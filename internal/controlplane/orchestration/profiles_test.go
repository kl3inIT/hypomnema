package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

// Scenario: the owner changes a profile. The change is a new version the
// name points at; the same source again changes nothing; a Dispatch keeps
// the version it ran with, and its worker gets the profile's instruction.
func TestAChangedProfileIsANewVersion(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	w.tasks(NewTask{Ref: "a", Spec: "a"})
	first := w.dispatchOf(w.schedule().Created[0])

	source := "name: implementer\ndescription: d\nruntime: claude\nmodel:\n  provider: model-gateway\n" +
		"  name: claude-sonnet-5-5\n  effort: high\ninstruction: Run the tests before finishing.\n"

	v2, created, err := w.s.Profiles().ApplyProfile(ctx, "", source)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 6, v2.Version, "after the four seeded and the world's own")
	assert.Equal(t, "model-gateway", v2.Provider)

	same, created, err := w.s.Profiles().ApplyProfile(ctx, "", source)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, v2.VersionID, same.VersionID)

	versions, err := w.s.Profiles().ListProfileVersions(ctx, "implementer")
	require.NoError(t, err)
	require.Len(t, versions, 6)
	assert.Equal(t, 6, versions[0].Version, "newest first")

	w.tasks(NewTask{Ref: "b", Spec: "b"})
	second := w.dispatchOf(w.schedule().Created[0])

	assert.NotEqual(t, v2.VersionID, w.dispatchOf(first.ID).ProfileVersionID, "the first keeps its version")
	assert.Equal(t, v2.VersionID, second.ProfileVersionID)
	assert.Equal(t, "claude-sonnet-5-5", second.Model)

	pending, err := w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)

	instructions := map[string]string{}
	for _, o := range pending.Offers {
		instructions[o.DispatchID] = o.Instruction
	}

	assert.Equal(t, "Run the tests before finishing.", instructions[second.ID])
	assert.Empty(t, instructions[first.ID])
}

// A provider names where its key is kept, never the key; and it cannot drop
// a model or a kind a profile depends on.
func TestAProviderKeepsItsProfilesWorking(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.Profiles().ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\napi_key_ref: sk-live-123\n")
	require.ErrorIs(t, err, ErrInvalid, "a pasted key is refused")

	router := "name: router\nkind: openai_compatible\napi_key_ref: ROUTER_KEY\nmodels: [m1, m2]\n"
	p, err := w.s.Profiles().ApplyProvider(ctx, "router", router)
	require.NoError(t, err)
	assert.Equal(t, []string{"m1", "m2"}, p.Models)

	_, err = w.s.PutBot(ctx, Bot{Name: DefaultBotID, Provider: "router", Model: "m1"})
	require.NoError(t, err)

	_, err = w.s.Profiles().ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\nmodels: [m2]\n")
	require.ErrorIs(t, err, ErrConflict, "the bot coordinator uses m1")

	_, _, err = w.s.Profiles().ApplyProfile(ctx, "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {provider: router, name: m2}\n")
	require.NoError(t, err)

	_, err = w.s.Profiles().ApplyProvider(ctx, "", "name: router\nkind: anthropic\nmodels: [m1, m2]\n")
	require.ErrorIs(t, err, ErrConflict, "codex cannot run against an API of Anthropic's")

	p, err = w.s.Profiles().ApplyProvider(ctx, "", "name: router\nkind: codex_subscription\nmodels: [m1, m2, m3]\n")
	require.NoError(t, err, "pi and codex both reach a codex subscription through the gateway")
	assert.Equal(t, []string{"m1", "m2", "m3"}, p.Models, "the models are replaced")

	models, err := w.s.Profiles().ListModels(ctx)
	require.NoError(t, err)
	assert.Contains(t, models, profilestore.Model{Provider: "router", Name: "m3",
		Kind: profilestore.ProviderCodexSubscription})
}

// A Task is checked against its profile when it is created; one that names
// none runs as an implementer.
func TestATaskIsCheckedAgainstItsProfile(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	created := w.tasks(NewTask{Ref: "a", Spec: "a"})
	assert.Equal(t, profilestore.DefaultProfile, created[0].Profile)

	for nt, want := range map[*NewTask]string{
		{Ref: "b", Profile: "designer", Spec: "b"}: "not found",
		{Ref: "c", Spec: "c", Model: "gpt-5"}: "named without its provider; name it <provider>/<model>; " +
			"no provider of kind model_gateway offers it",
		{Ref: "d", Spec: "d", Effort: "huge"}:            "unknown effort",
		{Ref: "e", Spec: "e", Runtime: "gemini"}:         "unknown runtime",
		{Ref: "f", Profile: "reviewer", Spec: "   "}:     "needs a spec",
		{Ref: "g", Spec: "g", Runtime: "pi", Model: "x"}: "name it <provider>/<model>; no provider runtime pi",
		{Ref: "i", Spec: "i", Model: "claude-haiku-5-5"}: "name it <provider>/<model>, such as " +
			"claude-subscription/claude-haiku-5-5",
		{Ref: "j", Spec: "j", Model: "nobody/claude-haiku-5-5"}: "not found: provider nobody",
		// A name with an empty part is refused, not taken for no model (review of PR #1000).
		{Ref: "k", Spec: "k", Model: "opencode-go/"}: `model "opencode-go/" has an empty part; ` +
			"name it <provider>/<model>",
		{Ref: "l", Spec: "l", Model: "/"}:                 `model "/" has an empty part`,
		{Ref: "m", Spec: "m", Model: "/claude-haiku-5-5"}: "has an empty part",
	} {
		_, err := w.s.CreateTasks(t.Context(), w.run.ID, []NewTask{*nt})
		require.ErrorContains(t, err, want, nt.Ref)
	}

	ok := w.tasks(NewTask{
		Ref: "h", Profile: "reviewer", Spec: "h", Model: "claude-subscription/claude-haiku-5-5", Effort: "max",
	})
	assert.Equal(t, "reviewer", ok[0].Profile)
}

// Scenario: a database with Claude Code's short names on the Claude
// subscription, as 00008 and 00043 seeded them, a gateway model opus served
// from it, another model served from its fable, and a key that may use opus
// and that model (issue #410). After the migration only the full models are
// offered, the YAML lists no short name, opus is no gateway model, the other
// model is served from the full Fable and the key keeps only what is left. The
// way back offers the seeded short names again.
func TestMigrationRemovesClaudesShortNames(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, p := databaseAt(t, 69)

	_, err := db.ExecContext(ctx, `
		INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
		VALUES ('opus', 'chat', 'name: opus', 1, 1), ('top', 'chat', 'name: top', 1, 1);
		INSERT INTO gateway_model_targets (model, position, provider, upstream_model)
		VALUES ('opus', 0, 'claude-subscription', 'opus'), ('top', 0, 'claude-subscription', 'fable');
		INSERT INTO gateway_keys (id, name, key_hash, models, created_at)
		VALUES ('gk_1', 'pi', 'h', '["opus","top"]', 1);`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_1" })

	claude, err := s.Profiles().GetProvider(ctx, "claude-subscription")
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"}, claude.Models)
	assert.Contains(t, claude.Source, "models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5]\n")

	var models, upstream string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT group_concat(name) FROM gateway_models WHERE name IN ('opus', 'top')`).Scan(&models))
	assert.Equal(t, "top", models)
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT upstream_model FROM gateway_model_targets WHERE model = 'top'`).Scan(&upstream))
	assert.Equal(t, "claude-fable-5-2", upstream)

	var allowed string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT models FROM gateway_keys WHERE id = 'gk_1'`).Scan(&allowed))
	assert.JSONEq(t, `["top"]`, allowed)

	_, err = p.DownTo(ctx, 69)
	require.NoError(t, err)

	claude, err = s.Profiles().GetProvider(ctx, "claude-subscription")
	require.NoError(t, err)
	assert.Subset(t, claude.Models, []string{"opus", "sonnet", "haiku", "fable"})
	assert.Contains(t, claude.Source,
		"models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, opus, sonnet, haiku]\n")
}

// Scenario (review of PR #1000, round 3): a version from before migration 00123, whose YAML still writes the old
// allowlist of web tools only, as 00123 left it beside the policy it re-expressed (every other family off). Restoring
// it keeps node_exec off, as the page shows the version, and never widens the policy to the old YAML read anew.
func TestARestoredVersionKeepsItsMigratedToolPolicy(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	old, _, err := w.s.Profiles().ApplyProfile(ctx, "", "name: x\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription, name: claude-haiku-5-5}\npreset: full\ntools_off: [node_*]\n")
	require.NoError(t, err)

	// As 00123 left it: the YAML as written then, the policy re-expressed in the columns.
	written := "name: x\ndescription: d\nruntime: claude\n" +
		"model: {provider: claude-subscription, name: claude-haiku-5-5}\ntools: [web_search, web_fetch]\n"
	_, err = w.db.ExecContext(ctx, `UPDATE profile_versions SET source = ?, digest = ? WHERE id = ?`,
		written, profilestore.SourceDigest(written), old.VersionID)
	require.NoError(t, err)

	_, _, err = w.s.Profiles().ApplyProfile(ctx, "", "name: x\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription, name: claude-opus-5-5}\n")
	require.NoError(t, err)

	restored, _, err := w.s.RestoreProfileVersion(ctx, "", "x", old.VersionID)
	require.NoError(t, err)
	assert.Equal(t, "full", restored.Preset)
	assert.Equal(t, []string{"node_*"}, restored.ToolsOff, "node_exec stays off")
	assert.Equal(t, "claude-haiku-5-5", restored.Model)
}
