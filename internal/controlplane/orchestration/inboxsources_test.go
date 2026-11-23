package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// beforeInboxSources is the last migration before the Gmail and meeting sources (00164, issue #1105).
const beforeInboxSources = 163

// Scenario: issue #1105. The Inbox's table is rebuilt for the Gmail and meeting sources, keeping every item and its
// history; the way back removes the items from Gmail or a meeting, with their history and an item marked a duplicate
// of one, and keeps the others with theirs.
func TestMigrationAddsTheGmailAndMeetingSources(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, beforeInboxSources)
	require.NoError(t, err)

	exec := func(stmts ...string) {
		t.Helper()

		for _, stmt := range stmts {
			_, err := db.ExecContext(ctx, stmt)
			require.NoError(t, err, stmt)
		}
	}
	count := func(query string) int {
		t.Helper()

		var n int
		require.NoError(t, db.QueryRowContext(ctx, query).Scan(&n), query)

		return n
	}

	exec(`INSERT INTO todos (id, title, state, source_kind, source_id, source_account, source_ref, created_by_kind,
			created_at, updated_at) VALUES ('tdo_wa', 'Reply to Kai', 'open', 'whatsapp', '659@s.whatsapp.net', 'main',
			'{"kind":"whatsapp_message","ref":"3EB0"}', 'bot', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, created_at, updated_at)
			VALUES ('tdo_hand', 'Renew the passport', 'open', 'owner', 'owner', 1, 1)`,
		`INSERT INTO todo_events (todo_id, at, revision, actor_kind, kind) VALUES ('tdo_wa', 1, 1, 'bot', 'created')`)

	_, err = db.ExecContext(ctx, `INSERT INTO todos (id, title, state, source_kind, source_id, source_account,
		source_ref, created_by_kind, created_at, updated_at) VALUES ('tdo_x', 'x', 'open', 'gmail', 't_1', 'work',
		'{"kind":"mail","ref":"m_1"}', 'bot', 1, 1)`)
	require.Error(t, err, "no gmail source before")

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))
	assert.Equal(t, 2, count(`SELECT count(*) FROM todos`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM todo_events WHERE todo_id = 'tdo_wa'`))

	exec(`INSERT INTO todos (id, title, state, source_kind, source_id, source_account, source_ref, created_by_kind,
			created_at, updated_at) VALUES ('tdo_poc', 'Read the PoC', 'proposed', 'gmail', 't_1', 'work',
			'{"kind":"mail","ref":"m_1"}', 'bot', 2, 2)`,
		`INSERT INTO todos (id, title, state, source_kind, source_id, source_ref, created_by_kind, created_at,
			updated_at) VALUES ('tdo_tiles', 'Check tiles', 'open', 'meeting', 'mtg_1',
			'{"kind":"meeting_line","ref":"mtg_1/12"}', 'bot', 2, 2)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, duplicate_of, closed_at, created_at,
			updated_at) VALUES ('tdo_dup', 'Read the PoC again', 'duplicate', 'owner', 'owner', 'tdo_poc', 3, 3, 3)`,
		`INSERT INTO todo_events (todo_id, at, revision, actor_kind, kind) VALUES ('tdo_poc', 2, 1, 'bot', 'created')`)

	for name, stmt := range map[string]string{
		"gmail without its account": `INSERT INTO todos (id, title, state, source_kind, source_id, source_ref,
			created_by_kind, created_at, updated_at) VALUES ('tdo_a', 'a', 'open', 'gmail', 't_2',
			'{"kind":"mail","ref":"m_2"}', 'bot', 1, 1)`,
		"a meeting with an account": `INSERT INTO todos (id, title, state, source_kind, source_id, source_account,
			created_by_kind, created_at, updated_at) VALUES ('tdo_b', 'b', 'open', 'meeting', 'mtg_1', 'work', 'bot', 1,
			1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.Error(t, err, name)
	}

	_, err = p.DownTo(ctx, beforeInboxSources)
	require.NoError(t, err)

	var ids []string

	rows, err := db.QueryContext(ctx, `SELECT id FROM todos ORDER BY id`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))

		ids = append(ids, id)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"tdo_hand", "tdo_wa"}, ids)
	assert.Equal(t, 1, count(`SELECT count(*) FROM todo_events`), "the kept item's history")

	_, err = p.Up(ctx)
	require.NoError(t, err)
}
