package orchestration

import (
	"database/sql"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboxWaitingVersions are the versions of the migration before issue #1142's and of its own, found by its name: it
// is named by its time until it is numbered right before its merge.
func inboxWaitingVersions(t *testing.T) (before, own int64) {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_inbox_why_open_waiting.sql") {
			return before, version
		}

		before = version
	}

	require.FailNow(t, "no inbox_why_open_waiting migration")

	return 0, 0
}

func todoColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `SELECT name FROM pragma_table_info('todos') ORDER BY cid`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	var names []string

	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}

	require.NoError(t, rows.Err())

	return names
}

// Scenario: issue #1142. The rebuild of todos for why_open waiting keeps every column in its order, every item with
// its chat, the Bot it was passed through, its why_open, priority and project, every event and every index; the check
// then takes waiting and nothing else unknown. The way back keeps the items, a waiting one without a why, and the
// migration before it can still drop the columns it added.
func TestMigrationLetsAnItemWaitOnAPerson(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	before, own := inboxWaitingVersions(t)
	db, p := databaseAt(t, before)

	columns := todoColumns(t, db)

	for _, stmt := range []string{
		`INSERT INTO inbox_projects (id, name, color, created_at, updated_at) VALUES ('prj_a', 'VMAP', '#2f80ed', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, source_id, source_account, source_ref, created_by_kind,
			created_by, why_open, chat_id, passed_by, priority, priority_reason, project_id, assignee_kind, assignee,
			created_at, updated_at)
		 VALUES ('tdo_a', 'Reply to Lan', 'open', 'zalo', 'team', 'main', '{"kind":"zalo_message","ref":"m1"}', 'bot',
			'bot_kai', 'asked_directly', 'cht_1', 'bot_main', 2, 'his boss', 'prj_a', 'bot', 'bot_kai', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, created_at, updated_at)
		 VALUES ('tdo_b', 'Call the bank', 'open', 'owner', 'owner', 1, 1)`,
		`INSERT INTO todo_events (todo_id, at, revision, actor_kind, actor, kind, before, after)
		 VALUES ('tdo_a', 1, 1, 'bot', 'bot_kai', 'created', '{}', '{"title":"Reply to Lan"}')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	assert.Equal(t, columns, todoColumns(t, db), "every column, in its order")

	var (
		why, chat, passed, reason, project string
		priority                           int
		events                             int
	)

	require.NoError(t, db.QueryRowContext(ctx, `SELECT why_open, chat_id, passed_by, priority, priority_reason,
		project_id FROM todos WHERE id = 'tdo_a'`).Scan(&why, &chat, &passed, &priority, &reason, &project))
	assert.Equal(t, []any{"asked_directly", "cht_1", "bot_main", 2, "his boss", "prj_a"},
		[]any{why, chat, passed, priority, reason, project})

	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM todo_events WHERE todo_id = 'tdo_a'`).
		Scan(&events))
	assert.Equal(t, 1, events, "the events are kept")

	for _, index := range []string{
		"todos_idempotency", "todos_state_due", "todos_duplicate_of", "todos_approval", "todos_attempt", "todos_work",
		"todos_chat", "todos_project", "todo_events_todo",
	} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index'
			AND name = ?`, index).Scan(&n))
		assert.Equal(t, 1, n, index)
	}

	_, err = db.ExecContext(ctx, `UPDATE todos SET why_open = 'waiting', assignee_kind = 'person', assignee = 'Việt'
		WHERE id = 'tdo_b'`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `UPDATE todos SET why_open = 'foo' WHERE id = 'tdo_a'`)
	require.Error(t, err, "an unknown why")

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	var gone sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT why_open, assignee FROM todos WHERE id = 'tdo_b'`).
		Scan(&gone, &reason))
	assert.False(t, gone.Valid, "waiting has no place before")
	assert.Equal(t, "Việt", reason, "the item stays given to the person")

	require.NoError(t, db.QueryRowContext(ctx, `SELECT why_open FROM todos WHERE id = 'tdo_a'`).Scan(&why))
	assert.Equal(t, "asked_directly", why)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM todo_events`).Scan(&events))
	assert.Equal(t, 1, events)

	_, err = p.DownTo(ctx, before-1)
	require.NoError(t, err, "the migration before drops its columns from the rebuilt table")
}
