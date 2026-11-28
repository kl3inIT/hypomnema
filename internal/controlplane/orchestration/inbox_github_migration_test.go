package orchestration

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: issue #1232. The rebuild of todos for GitHub's calls keeps every column in its order, every item with its
// project and events, and every index; the source then takes github, with its first call, and the calls have a table.
// The way back drops the items the calls made, with their events and an item marked their duplicate, keeps the
// rest, and the migration before it can still go back past it.
func TestMigrationLetsGitHubsCallsMakeItems(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	own := migrationVersion(t, "_inbox_github_calls.sql")
	before := versionBefore(t, own)
	db, p := databaseAt(t, before)

	columns := todoColumns(t, db)

	for _, stmt := range []string{
		`INSERT INTO departments (id, name, position, created_at) VALUES ('dep_a', 'VMAP', 0, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, source_id, source_account, source_ref, created_by_kind,
			created_by, project_id, assignee_kind, assignee, created_at, updated_at)
		 VALUES ('tdo_a', 'Reply to Lan', 'open', 'zalo', 'team', 'main', '{"kind":"zalo_message","ref":"m1"}', 'bot',
			'bot_kai', 'dep_a', 'bot', 'bot_kai', 1, 1)`,
		`INSERT INTO todo_events (todo_id, at, revision, actor_kind, actor, kind, before, after)
		 VALUES ('tdo_a', 1, 1, 'bot', 'bot_kai', 'created', '{}', '{"title":"Reply to Lan"}')`,
		`INSERT INTO github_deliveries (id, event, action, repository, payload, received_at)
		 VALUES ('d-old', 'issue_comment', 'created', 'kl3init/hypomnema', '{"comment":{"body":"words"}}', 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	assert.Equal(t, columns, todoColumns(t, db), "every column, in its order")

	var kept int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM github_deliveries WHERE payload IS NOT NULL`).
		Scan(&kept))
	assert.Zero(t, kept, "no delivery's body is kept, those of before neither")

	var (
		project string
		events  int
	)

	require.NoError(t, db.QueryRowContext(ctx, `SELECT project_id FROM todos WHERE id = 'tdo_a'`).Scan(&project))
	assert.Equal(t, "dep_a", project)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM todo_events`).Scan(&events))
	assert.Equal(t, 1, events, "the events are kept")

	for _, index := range []string{
		"todos_idempotency", "todos_state_due", "todos_duplicate_of", "todos_approval", "todos_attempt", "todos_work",
		"todos_chat", "todos_project", "todo_events_todo", "todo_github_calls_todo", "todo_github_calls_unreplied",
	} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index'
			AND name = ?`, index).Scan(&n))
		assert.Equal(t, 1, n, index)
	}

	_, err = db.ExecContext(ctx, `INSERT INTO todos (id, title, state, source_kind, source_id, created_by_kind,
		created_at, updated_at) VALUES ('tdo_x', 'No call', 'open', 'github', 'kl3init/hypomnema#1', 'system', 1, 1)`)
	require.Error(t, err, "an item of a call names the call")

	for _, stmt := range []string{
		`INSERT INTO todos (id, title, state, source_kind, source_id, source_ref, created_by_kind, created_by,
			assignee_kind, assignee, created_at, updated_at)
		 VALUES ('tdo_g', 'kl3init/hypomnema#1269', 'open', 'github', 'kl3init/hypomnema#1269',
			'{"kind":"url","ref":"https://github.com/kl3inIT/hypomnema/pull/1269#issuecomment-1"}', 'system', 'github',
			'bot', 'bot_main', 2, 2)`,
		`INSERT INTO todo_events (todo_id, at, revision, actor_kind, actor, kind, before, after)
		 VALUES ('tdo_g', 2, 1, 'system', 'github', 'created', '{}', '{}')`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, duplicate_of, closed_at, created_at,
			updated_at) VALUES ('tdo_d', 'Same', 'duplicate', 'owner', 'owner', 'tdo_g', 3, 3, 3)`,
		`INSERT INTO todo_github_calls (delivery, todo_id, repository, number, kind, comment_id, login, url, created_at)
		 VALUES ('d1', 'tdo_g', 'kl3init/hypomnema', 1269, 'comment', 1, 'kl3inIT',
			'https://github.com/kl3inIT/hypomnema/pull/1269#issuecomment-1', 2)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err = p.DownTo(ctx, before)
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
	assert.Equal(t, []string{"tdo_a"}, ids, "the calls' items and their duplicate go")

	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM todo_events`).Scan(&events))
	assert.Equal(t, 1, events)

	_, err = p.DownTo(ctx, versionBefore(t, before))
	require.NoError(t, err, "the migration before goes back past the rebuilt table")
}

// versionBefore is the version of the migration before version: they are named by their time until they are numbered
// right before the merge.
func versionBefore(t *testing.T, version int64) int64 {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	var before int64

	for _, e := range entries {
		v, err := goose.NumericComponent(e.Name())
		require.NoError(t, err)

		if v < version && v > before {
			before = v
		}
	}

	return before
}

// Scenario (review of PR #1323 after #1337): the rebuild of todos keeps every row of the tables with a foreign key
// to it, which SQLite would delete with the old todos (ON DELETE CASCADE): a review that reached its limit keeps its
// Run, pull request and long report (review_stall_todos, 00193), after the migration, and after its way back and up
// again.
func TestMigrationKeepsTheReviewStallOfAnItem(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	own := migrationVersion(t, "_inbox_github_calls.sql")
	before := versionBefore(t, own)
	db, p := databaseAt(t, before)

	for _, stmt := range []string{
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at)
		 VALUES ('run_a', 'o', 'open', 'owner', 1, 1)`,
		`INSERT INTO todos (id, title, state, source_kind, created_by_kind, created_at, updated_at)
		 VALUES ('tdo_s', 'Review limit reached', 'open', 'owner', 'owner', 1, 1)`,
		`INSERT INTO review_stall_todos (run_id, head_sha, pull_request, todo_id, long_report)
		 VALUES ('run_a', 'abc123', 'https://github.com/kl3inIT/hypomnema/pull/1', 'tdo_s', 'the long report')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	stall := func() string {
		var run, head, pr, report string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT run_id, head_sha, pull_request, long_report
			FROM review_stall_todos WHERE todo_id = 'tdo_s'`).Scan(&run, &head, &pr, &report))

		return strings.Join([]string{run, head, pr, report}, "|")
	}

	want := "run_a|abc123|https://github.com/kl3inIT/hypomnema/pull/1|the long report"

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)
	assert.Equal(t, want, stall(), "after Up")

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)
	assert.Equal(t, want, stall(), "after Down")

	_, err = p.UpTo(ctx, own)
	require.NoError(t, err)
	assert.Equal(t, want, stall(), "after Down then Up")
}
