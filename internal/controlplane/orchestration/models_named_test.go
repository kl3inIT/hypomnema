package orchestration

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a database from before issue #1002, with Tasks, a profile version, a Bot and a chat naming models
// without their provider. The migration gives each the provider it resolves to today: a Task the one its Dispatch
// ran under, else the one provider that offers the name; a name two providers offer, or none, is left as it is.
// Afterwards nothing stored that could be placed is bare, and the way back takes a Task's provider off again.
func TestMigrationNamesStoredModelsWithTheirProvider(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)

	version := migrationVersion(t, p, "_models_named_with_provider.sql")
	_, err = p.UpTo(ctx, version-1)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT OR IGNORE INTO models (provider, name) VALUES ('claude-subscription', 'claude-opus-5-5'),
		 ('claude-subscription', 'claude-haiku-5-5'), ('opencode-go', 'claude-haiku-5-5'),
		 ('opencode-go', 'deepseek-v4-pro'), ('codex-subscription', 'gpt-6.1-sol')`,
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`,
		// ran: dispatched under the Claude subscription; unique: one provider offers it; twice: two providers do;
		// unknown: none does; named: already "<provider>/<model>"; none: no model of its own.
		`INSERT INTO tasks (id, run_id, position, profile, spec, status, model, created_at, updated_at) VALUES
		 ('ran', 'r', 1, 'implementer', 's', 'succeeded', 'claude-opus-5-5', 1, 1),
		 ('unique', 'r', 2, 'implementer', 's', 'ready', 'deepseek-v4-pro', 1, 1),
		 ('twice', 'r', 3, 'implementer', 's', 'ready', 'claude-haiku-5-5', 1, 1),
		 ('unknown', 'r', 4, 'implementer', 's', 'ready', 'no-such-model', 1, 1),
		 ('named', 'r', 5, 'implementer', 's', 'ready', 'codex-subscription/gpt-6.1-sol', 1, 1),
		 ('none', 'r', 6, 'implementer', 's', 'ready', NULL, 1, 1)`,
		`INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, status, created_at, updated_at)
		 VALUES ('c', 'ran', 'node_a', '/w', 'claude', 'gateway', 'open', 1, 1)`,
		`INSERT INTO dispatches (id, task_id, conversation_id, node_id, status, epoch, model, created_at, updated_at)
		 VALUES ('d', 'ran', 'c', 'node_a', 'succeeded', 1, 'claude-subscription/claude-opus-5-5', 1, 1)`,
		`INSERT INTO profile_versions (id, profile, version, source, digest, runtime, model, created_at, description)
		 VALUES ('pv', 'implementer', 99, 'name: implementer', 'x', 'omp', 'deepseek-v4-pro', 1, 'd')`,
		`INSERT INTO bots (id, name, model, created_at, updated_at) VALUES ('bot_x', 'x', 'deepseek-v4-pro', 1, 1)`,
		`INSERT INTO chats (id, title, bot_id, model, created_at, updated_at)
		 VALUES ('cht_x', 't', 'bot_x', 'claude-opus-5-5', 1, 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err = p.UpTo(ctx, version)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"ran": "claude-subscription/claude-opus-5-5", "unique": "opencode-go/deepseek-v4-pro",
		"twice": "claude-haiku-5-5", "unknown": "no-such-model", "named": "codex-subscription/gpt-6.1-sol", "none": "",
	}, taskModels(t, db), "a name two providers offer, or none, is not guessed")

	var pv, bot, chat sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT provider FROM profile_versions WHERE id = 'pv'`).Scan(&pv))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT provider FROM bots WHERE id = 'bot_x'`).Scan(&bot))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT provider FROM chats WHERE id = 'cht_x'`).Scan(&chat))
	assert.Equal(t, []string{"opencode-go", "opencode-go", "claude-subscription"},
		[]string{pv.String, bot.String, chat.String})

	// Every stored model that names no provider is one of those left for the owner.
	var bare int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM tasks t WHERE t.model IS NOT NULL AND t.model <> ''
		          AND t.id NOT IN ('twice', 'unknown')
		          AND NOT EXISTS (SELECT 1 FROM providers p WHERE substr(t.model, 1, length(p.name) + 1) = p.name || '/'))
		     + (SELECT count(*) FROM profile_versions WHERE model IS NOT NULL AND provider IS NULL)
		     + (SELECT count(*) FROM bots WHERE model IS NOT NULL AND provider IS NULL)
		     + (SELECT count(*) FROM chats WHERE model IS NOT NULL AND provider IS NULL)`).Scan(&bare))
	assert.Zero(t, bare)

	_, err = p.Down(ctx)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", taskModels(t, db)["ran"], "the way back reads a bare name again")
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
