package orchestration

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
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
	version := migrationVersion(t, modelNamesMigration)
	db, p := databaseAt(t, version-1)

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
	_, _, err := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" }).
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
	db, p := databaseAt(t, migrationVersion(t, "_opencode_zen_provider.sql")-1)

	svc := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" })
	_, err := svc.ApplyProvider(ctx, "", "name: opencode-zen\nkind: openai_compatible\n"+
		"base_url: https://zen.example/v1\napi_key_ref: ZEN_KEY\nmodels: [mine]\n")
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	got, err := svc.GetProvider(ctx, "opencode-zen")
	require.NoError(t, err)
	assert.Equal(t, ProviderOpenAICompatible, got.Kind)
	assert.Equal(t, []string{"mine"}, got.Models)

	_, err = p.DownTo(ctx, migrationVersion(t, "_opencode_zen_provider.sql")-1)
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
	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

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
	_, err = p.DownTo(ctx, migrationVersion(t, modelNamesMigration)-1)
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

// Scenario (review of PR #1000, round 4): the owner served Zen under a provider of their own, custom-zen, beside
// Zen's, and a role Task named its model custom-zen/only-zen; a Bot's fallbacks hold model names with a quote. Taking
// the database back to the code before keeps custom-zen/only-zen whole, as the code before would place the bare
// only-zen nowhere (it has no Zen); a role Task's codex-subscription/gpt-6.2-sol, which every runtime taking the Codex
// subscription would place there alone, loses its provider; and Zen's way back drops the Zen fallback and keeps the
// other, quote and all.
func TestTheWayBackKeepsAProviderTheCodeBeforeCouldNotPlace(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db, &fakeClock{now: t0}, platform.NewID)
	_, err = svc.ApplyProvider(ctx, "", "name: custom-zen\nkind: opencode_zen\nmodels: [only-zen]\n")
	require.NoError(t, err)
	_, err = svc.AddProviderModels(ctx, "codex-subscription", []string{"gpt-6.2-sol"})
	require.NoError(t, err)
	_, err = svc.AddProviderModels(ctx, "opencode-zen", []string{`foo"bar`})
	require.NoError(t, err)
	_, err = svc.AddProviderModels(ctx, "opencode-go", []string{`we"ird\`})
	require.NoError(t, err)

	// implementer as a role profile, its chain read from role routing (deploy/profiles).
	_, _, err = svc.ApplyProfile(ctx, "", "name: implementer\ndescription: d\n")
	require.NoError(t, err)

	bot, err := svc.PutBot(ctx, Bot{
		Name: "fallbacks", Provider: "opencode-go", Model: "deepseek-v4-pro",
		Fallbacks: []string{`opencode-zen/foo"bar`, `opencode-go/we"ird\`},
	})
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`)
	require.NoError(t, err)

	made, err := svc.CreateTasks(ctx, "r", []NewTask{
		{Ref: "custom", Spec: "s", Profile: "implementer", Model: "custom-zen/only-zen"},
		{Ref: "codex", Spec: "s", Profile: "implementer", Model: "codex-subscription/gpt-6.2-sol"},
	})
	require.NoError(t, err)

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.DownTo(ctx, migrationVersion(t, modelNamesMigration)-1)
	require.NoError(t, err, "a fallback with a quote does not stop the way back")

	models := taskModels(t, db)
	assert.Equal(t, "custom-zen/only-zen", models[made[0].ID], "the code before has no Zen: the provider is kept")
	assert.Equal(t, "gpt-6.2-sol", models[made[1].ID], "placed at the Codex subscription by any runtime")

	var fallbacks string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT fallbacks FROM bots WHERE id = ?`, bot.ID).Scan(&fallbacks))
	assert.Equal(t, `opencode-go/we"ird\`, fallbacks)
}

// Scenario (review of PR #1000, round 6): profile versions, Bots and chats stored with a model and no provider. Each
// gets the one provider that offers its model to its runtime (a profile version's, pi's for a Bot and a chat); one
// two providers or none offer is left, and nothing that could be placed is left bare in any table. The way back
// leaves each without its provider again while it names what the conversion left it with; one changed since keeps it.
func TestMigrationNamesProviderlessRowsOfEveryTable(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	version := migrationVersion(t, modelNamesMigration)
	db, p := databaseAt(t, version-1)

	for _, stmt := range []string{
		`INSERT OR IGNORE INTO models (provider, name) VALUES
		 ('codex-subscription', 'gpt-6.1-sol'), ('opencode-go', 'gpt-6.1-sol'),
		 ('claude-subscription', 'claude-haiku-5-5'), ('opencode-go', 'claude-haiku-5-5'),
		 ('opencode-go', 'deepseek-v4-pro')`,
		`INSERT INTO profile_versions (id, profile, version, source, digest, runtime, model, created_at, description)
		 VALUES ('pv-codex', 'implementer', 91, 'name: implementer', 'a', 'codex', 'gpt-6.1-sol', 1, 'd'),
		        ('pv-twice', 'implementer', 92, 'name: implementer', 'b', 'claude', 'claude-haiku-5-5', 1, 'd'),
		        ('pv-none', 'implementer', 93, 'name: implementer', 'c', 'claude', 'no-such-model', 1, 'd')`,
		`INSERT INTO bots (id, name, model, created_at, updated_at) VALUES
		 ('bot-go', 'go', 'deepseek-v4-pro', 1, 1), ('bot-twice', 'twice', 'claude-haiku-5-5', 1, 1),
		 ('bot-none', 'none', 'no-such-model', 1, 1), ('bot-default', 'default', NULL, 1, 1)`,
		`INSERT INTO chats (id, title, bot_id, model, created_at, updated_at) VALUES
		 ('cht-go', 't', 'bot-go', 'deepseek-v4-pro', 1, 1), ('cht-twice', 't', 'bot-go', 'claude-haiku-5-5', 1, 1),
		 ('cht-bots', 't', 'bot-go', NULL, 1, 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, Migrate(ctx, db))

	queries := map[string]string{
		"profile_versions": `SELECT id, coalesce(provider, '') FROM profile_versions WHERE id LIKE '%-%'`,
		"bots":             `SELECT id, coalesce(provider, '') FROM bots WHERE id LIKE '%-%'`,
		"chats":            `SELECT id, coalesce(provider, '') FROM chats WHERE id LIKE '%-%'`,
	}
	providers := func(table string) map[string]string {
		rows, err := db.QueryContext(ctx, queries[table])
		require.NoError(t, err)
		defer rows.Close() //nolint:errcheck // read only

		out := map[string]string{}
		for rows.Next() {
			var id, provider string
			require.NoError(t, rows.Scan(&id, &provider))
			out[id] = provider
		}
		require.NoError(t, rows.Err())

		return out
	}

	assert.Equal(t, map[string]string{"pv-codex": "codex-subscription", "pv-twice": "", "pv-none": ""},
		providers("profile_versions"), "Go's kind is not one Codex runs against")
	assert.Equal(t, map[string]string{"bot-go": "opencode-go", "bot-twice": "", "bot-none": "", "bot-default": ""},
		providers("bots"))
	assert.Equal(t, map[string]string{"cht-go": "opencode-go", "cht-twice": "", "cht-bots": ""}, providers("chats"))

	// Every row of every table that names a model and no provider is one two providers or none offer.
	var left []string
	rows, err := db.QueryContext(ctx, `
		SELECT id FROM profile_versions WHERE model <> '' AND provider IS NULL
		UNION ALL SELECT id FROM bots WHERE model <> '' AND provider IS NULL
		UNION ALL SELECT id FROM chats WHERE model <> '' AND provider IS NULL
		UNION ALL SELECT id FROM tasks WHERE model <> '' AND model_unresolved = 0
		  AND NOT EXISTS (SELECT 1 FROM providers p WHERE substr(tasks.model, 1, length(p.name) + 1) = p.name || '/')
		ORDER BY 1`)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck // read only
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		left = append(left, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"bot-none", "bot-twice", "cht-twice", "pv-none", "pv-twice"}, left)

	// The Bot is changed after the upgrade; the chat is not.
	_, err = db.ExecContext(ctx, `UPDATE bots SET model = 'kimi-k3' WHERE id = 'bot-go'`)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, version-1)
	require.NoError(t, err)
	assert.Empty(t, providers("profile_versions")["pv-codex"], "left without it again, as it ran")
	assert.Empty(t, providers("chats")["cht-go"])
	assert.Equal(t, "opencode-go", providers("bots")["bot-go"], "a Bot changed since keeps what it names")
}
