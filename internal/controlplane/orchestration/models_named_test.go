package orchestration

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
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
		 ('opencode-go', 'deepseek-v4-pro'),
		 ('claude-subscription', 'opencode-go/twice'), ('opencode-go', 'opencode-go/twice'), ('opencode-go', 'twice'),
		 ('opencode-go', 'bar')`,
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`,
		`INSERT INTO tasks (id, run_id, position, profile, kind, spec, status, runtime, model, created_at, updated_at) VALUES
		 ('codex', 'r', 1, 'implementer', 'broad', 's', 'ready', 'codex', 'gpt-6.1-sol', 1, 1),
		 ('slash', 'r', 2, 'implementer', 'broad', 's', 'ready', 'claude', 'opencode-go/foo', 1, 1),
		 ('narrow', 'r', 3, 'implementer', 'narrow', 's', 'ready', NULL, 'gpt-6.1-sol', 1, 1),
		 ('broad', 'r', 4, 'implementer', 'broad', 's', 'ready', NULL, 'gpt-6.1-sol', 1, 1),
		 ('twice', 'r', 5, 'implementer', 'broad', 's', 'ready', 'claude', 'claude-haiku-5-5', 1, 1),
		 ('unknown', 'r', 6, 'implementer', 'broad', 's', 'ready', 'omp', 'no-such-model', 1, 1),
		 ('go', 'r', 7, 'implementer', 'broad', 's', 'ready', 'omp', 'deepseek-v4-pro', 1, 1),
		 ('none', 'r', 8, 'implementer', 'broad', 's', 'ready', NULL, NULL, 1, 1),
		 ('slash-twice', 'r', 9, 'implementer', 'broad', 's', 'ready', 'claude', 'opencode-go/twice', 1, 1),
		 ('slash-unknown', 'r', 10, 'implementer', 'broad', 's', 'ready', 'claude', 'opencode-go/bar', 1, 1)`,
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
		// Two providers offer the whole name, or none does: left as it was, never read at its slash.
		"slash-twice": "opencode-go/twice", "slash-unknown": "opencode-go/bar",
	}, taskModels(t, db))

	var unresolved []string
	rows, err := db.QueryContext(ctx, `SELECT id FROM tasks WHERE model_unresolved = 1 ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck // read only
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		unresolved = append(unresolved, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"broad", "slash-twice", "slash-unknown", "twice", "unknown"}, unresolved)

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

	// A Task left unresolved is refused, as the code before refused it, not dispatched to Go's twice or bar.
	for _, id := range []string{"slash-twice", "slash-unknown"} {
		task, err := q.GetTask(ctx, id)
		require.NoError(t, err)

		_, err = resolve(ctx, q, task)
		require.ErrorIs(t, err, ErrInvalid, id)
		require.ErrorContains(t, err, "named without its provider", id)
	}

	// A Task made after the upgrade names its provider.
	svc := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" })
	made, err := svc.CreateTasks(ctx, "r", []NewTask{{
		Ref: "after", Spec: "s", Profile: "implementer", Runtime: "codex", Model: "codex-subscription/gpt-6.1-sol",
	}})
	require.NoError(t, err)
	before[made[0].ID] = "gpt-6.1-sol" // the bare name the code before reads, which it places at Codex

	require.NoError(t, Migrate(ctx, db), "a second run changes nothing")
	assert.Equal(t, "claude-subscription/opencode-go/foo", taskModels(t, db)["slash"])

	_, err = p.DownTo(ctx, version-1)
	require.NoError(t, err)
	assert.Equal(t, before, taskModels(t, db), "the way back restores each model and strips a new one's provider")

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	provider, ok, err := legacyProvider(ctx, tx, "codex", "gpt-6.1-sol")
	require.NoError(t, err)
	assert.Equal(t, "codex-subscription", provider, "the code before reads the new Task's model as it ran")
	assert.True(t, ok)
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

// Scenario (review of PR #1000): the owner made a provider named opencode-zen of another kind before upgrading. The
// upgrade does not stop on the name: Zen's seed is skipped, and that provider and its models are left as they were.
func TestTheZenSeedLeavesAnOwnersProviderOfThatName(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)

	_, err = p.UpTo(ctx, migrationVersion(t, p, "_opencode_zen_provider.sql")-1)
	require.NoError(t, err)

	svc := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" })
	_, err = svc.ApplyProvider(ctx, "", "name: opencode-zen\nkind: openai_compatible\n"+
		"base_url: https://zen.example/v1\napi_key_ref: ZEN_KEY\nmodels: [mine]\n")
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	got, err := svc.GetProvider(ctx, "opencode-zen")
	require.NoError(t, err)
	assert.Equal(t, ProviderOpenAICompatible, got.Kind)
	assert.Equal(t, []string{"mine"}, got.Models)

	_, err = p.DownTo(ctx, migrationVersion(t, p, "_opencode_zen_provider.sql")-1)
	require.NoError(t, err)

	got, err = svc.GetProvider(ctx, "opencode-zen")
	require.NoError(t, err)
	assert.Equal(t, []string{"mine"}, got.Models, "the way back leaves it too")
}

// Scenario (review of PR #1000, round 3): the owner used OpenCode Zen, then takes the database back to the code before
// both migrations. No foreign key stops it: a profile version, a Bot and a chat on Zen keep no model, a Bot's Zen
// fallback and the gateway's Zen target go, and a Task's Zen model is taken off. Each other Task's model loses its
// provider only where the code before would place the bare name at that same provider: a Codex Task's gpt-6.1-sol,
// which Go also lists but not to Codex, does; a Claude Task's claude-haiku-5-5, which Go also offers to Claude Code,
// keeps its provider.
func TestTheWayBackAfterZenLeavesRowsTheCodeBeforeRuns(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, Migrate(ctx, db))

	svc := New(db, &fakeClock{now: t0}, platform.NewID)
	_, err = svc.AddProviderModels(ctx, "opencode-go", []string{"gpt-6.1-sol", "claude-haiku-5-5"})
	require.NoError(t, err)
	_, err = svc.AddProviderModels(ctx, "codex-subscription", []string{"gpt-6.1-sol"})
	require.NoError(t, err)
	_, err = svc.AddProviderModels(ctx, "claude-subscription", []string{"claude-haiku-5-5"})
	require.NoError(t, err)

	zenProfile, _, err := svc.ApplyProfile(ctx, "", "name: zen\ndescription: d\nruntime: claude\n"+
		"model: {provider: opencode-zen, name: glm-5.3}\n")
	require.NoError(t, err)
	bot, err := svc.PutBot(ctx, Bot{
		Name: "zen", Provider: "opencode-zen", Model: "glm-5.3",
		Fallbacks: []string{"opencode-zen/kimi-k3", "opencode-go/deepseek-v4-pro"},
	})
	require.NoError(t, err)
	chat, err := svc.CreateChatWithID(ctx, "", bot.ID, "", ChatModel{Provider: "opencode-zen", Model: "kimi-k3"})
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO gateway_models (name, capability, source, created_at, updated_at) VALUES
		 ('zen-only', 'chat', 'name: zen-only', 1, 1), ('both', 'chat', 'name: both', 1, 1)`,
		`INSERT INTO gateway_model_targets (model, position, provider, upstream_model) VALUES
		 ('zen-only', 0, 'opencode-zen', 'glm-5.3'), ('both', 0, 'opencode-zen', 'glm-5.3'),
		 ('both', 1, 'opencode-go', 'glm-5.3')`,
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	made, err := svc.CreateTasks(ctx, "r", []NewTask{
		{Ref: "zen", Spec: "s", Profile: "implementer", Runtime: "claude", Model: "opencode-zen/glm-5.3"},
		{Ref: "codex", Spec: "s", Profile: "implementer", Runtime: "codex", Model: "codex-subscription/gpt-6.1-sol"},
		{Ref: "claude", Spec: "s", Profile: "implementer", Runtime: "claude",
			Model: "claude-subscription/claude-haiku-5-5"},
	})
	require.NoError(t, err)

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.DownTo(ctx, migrationVersion(t, p, modelNamesMigration)-1)
	require.NoError(t, err, "no foreign key stops the way back")

	models := taskModels(t, db)
	assert.Empty(t, models[made[0].ID], "the Zen Task runs with its profile's model")
	assert.Equal(t, "gpt-6.1-sol", models[made[1].ID], "placed at the Codex subscription again")
	assert.Equal(t, "claude-subscription/claude-haiku-5-5", models[made[2].ID],
		"Claude Code reaches two providers of the bare name: the provider is kept")

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	placed, ok, err := legacyProvider(ctx, tx, "codex", "gpt-6.1-sol")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "codex-subscription", placed)

	var zen, versionProvider, botModel, chatModel sql.NullString
	var fallbacks string
	var targets, gatewayModels int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT
		(SELECT name FROM providers WHERE name = 'opencode-zen'),
		(SELECT provider FROM profile_versions WHERE id = ?),
		(SELECT model FROM bots WHERE id = ?), (SELECT fallbacks FROM bots WHERE id = ?),
		(SELECT model FROM chats WHERE id = ?),
		(SELECT count(*) FROM gateway_model_targets WHERE provider = 'opencode-zen'),
		(SELECT count(*) FROM gateway_models WHERE name IN ('zen-only', 'both'))`,
		zenProfile.VersionID, bot.ID, bot.ID, chat.ID).
		Scan(&zen, &versionProvider, &botModel, &fallbacks, &chatModel, &targets, &gatewayModels))
	assert.False(t, zen.Valid, "Zen's provider is gone")
	assert.False(t, versionProvider.Valid)
	assert.False(t, botModel.Valid)
	assert.Equal(t, "opencode-go/deepseek-v4-pro", fallbacks)
	assert.False(t, chatModel.Valid)
	assert.Zero(t, targets)
	assert.Equal(t, 1, gatewayModels, "a gateway model with another target keeps it")
}
