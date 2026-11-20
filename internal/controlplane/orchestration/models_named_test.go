package orchestration

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario (issue #1002, review of PR #1000): a database from before every model was named with its provider. Each
// Task's bare model is given the provider the code before dispatched it under, read as it read it: among the kinds
// the Task's runtime runs against (a Codex Task's gpt-6.1-sol is the Codex subscription's though Go lists it too), a
// whole name with a slash in it as a name ("opencode-go/foo" that only the Claude subscription offers), a role Task's
// runtime from its chain's first entry. A name it would not have placed is left. Afterwards each converted Task
// dispatches where it did, and the way back restores every model it had.
func TestMigrationNamesTaskModelsAsTheyWereResolved(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)

	version := migrationVersion(t, p, modelNamesMigration)
	_, err = p.UpTo(ctx, version-1)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT OR IGNORE INTO models (provider, name) VALUES
		 ('codex-subscription', 'gpt-6.1-sol'), ('opencode-go', 'gpt-6.1-sol'),
		 ('claude-subscription', 'opencode-go/foo'), ('opencode-go', 'foo'),
		 ('claude-subscription', 'claude-haiku-5-5'), ('opencode-go', 'claude-haiku-5-5'),
		 ('opencode-go', 'deepseek-v4-pro')`,
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`,
		`INSERT INTO tasks (id, run_id, position, profile, kind, spec, status, runtime, model, created_at, updated_at) VALUES
		 ('codex', 'r', 1, 'implementer', 'broad', 's', 'ready', 'codex', 'gpt-6.1-sol', 1, 1),
		 ('slash', 'r', 2, 'implementer', 'broad', 's', 'ready', 'claude', 'opencode-go/foo', 1, 1),
		 ('narrow', 'r', 3, 'implementer', 'narrow', 's', 'ready', NULL, 'gpt-6.1-sol', 1, 1),
		 ('broad', 'r', 4, 'implementer', 'broad', 's', 'ready', NULL, 'gpt-6.1-sol', 1, 1),
		 ('twice', 'r', 5, 'implementer', 'broad', 's', 'ready', 'claude', 'claude-haiku-5-5', 1, 1),
		 ('unknown', 'r', 6, 'implementer', 'broad', 's', 'ready', 'omp', 'no-such-model', 1, 1),
		 ('go', 'r', 7, 'implementer', 'broad', 's', 'ready', 'omp', 'deepseek-v4-pro', 1, 1),
		 ('none', 'r', 8, 'implementer', 'broad', 's', 'ready', NULL, NULL, 1, 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	// implementer as a role profile, its chain read from role routing (deploy/profiles).
	_, _, err = New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" }).
		ApplyProfile(ctx, "", "name: implementer\ndescription: d\n")
	require.NoError(t, err)

	before := taskModels(t, db)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	assert.Equal(t, map[string]string{
		"codex":   "codex-subscription/gpt-6.1-sol",      // Go's kind is not one Codex runs against
		"slash":   "claude-subscription/opencode-go/foo", // the whole name, not Go's foo
		"narrow":  "codex-subscription/gpt-6.1-sol",      // the narrow chain begins with Codex
		"broad":   "gpt-6.1-sol",                         // Claude Code reaches both: it was not placed
		"twice":   "claude-haiku-5-5",
		"unknown": "no-such-model",
		"go":      "opencode-go/deepseek-v4-pro",
		"none":    "",
	}, taskModels(t, db))

	q := store.New(db)
	for id, want := range map[string]string{
		"codex": "codex-subscription/gpt-6.1-sol", "slash": "claude-subscription/opencode-go/foo",
	} {
		task, err := q.GetTask(ctx, id)
		require.NoError(t, err)

		r, err := resolve(ctx, q, task)
		require.NoError(t, err, id)
		assert.Equal(t, want, r.model, "%s dispatches where it did", id)
	}

	require.NoError(t, Migrate(ctx, db), "a second run changes nothing")
	assert.Equal(t, "claude-subscription/opencode-go/foo", taskModels(t, db)["slash"])

	_, err = p.DownTo(ctx, version-1)
	require.NoError(t, err)
	assert.Equal(t, before, taskModels(t, db), "the way back restores each model")
}

func taskModels(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `SELECT id, coalesce(model, '') FROM tasks`)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rows.Close() })

	out := map[string]string{}

	for rows.Next() {
		var id, model string
		require.NoError(t, rows.Scan(&id, &model))
		out[id] = model
	}

	require.NoError(t, rows.Err())

	return out
}
