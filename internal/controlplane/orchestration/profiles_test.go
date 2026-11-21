package orchestration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a fresh control plane has the starting set, and applying a
// seeded profile's own source changes nothing.
func TestTheStartingSetIsThere(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	profiles, err := w.s.ListProfiles(ctx)
	require.NoError(t, err)

	// The seed, the seed with its description, for the two that report in a set form that form (issue #155), and
	// the model gateway's model (issue #462), now its subscription's (issue #549).
	// The instructions rewritten (issue #672), and the reviewer told to answer CodeRabbit (issue #766) and to run
	// the checks (issue #718).
	versions := map[string]int{"explorer": 5, "implementer": 4, "reviewer": 7}
	models := map[string]string{
		"explorer": "claude-sonnet-5-5", "implementer": "claude-opus-5-5", "reviewer": "claude-opus-5-5",
	}

	names := map[string]string{}
	for _, p := range profiles {
		names[p.Name] = p.Runtime
		assert.NotEmpty(t, p.Description, "%s says what it is for", p.Name)
		assert.Equal(t, versions[p.Name], p.Version, p.Name)
		assert.Equal(t, []string{"claude-subscription", models[p.Name]}, []string{p.Provider, p.Model}, p.Name)

		// A seeded row holds what applying its own source would store.
		same, created, err := w.s.ApplyProfile(ctx, p.Name, p.Source)
		require.NoError(t, err, p.Name)
		assert.False(t, created, "%s: the digest is the source's", p.Name)
		assert.Equal(t, p.VersionID, same.VersionID, p.Name)

		copied, _, err := w.s.ApplyProfile(ctx, "",
			strings.Replace(p.Source, "name: "+p.Name+"\n", "name: copy-"+p.Name+"\n", 1))
		require.NoError(t, err, p.Name)
		assert.Equal(t, []string{p.Description, p.Runtime, p.Model, p.Effort, p.Instruction},
			[]string{copied.Description, copied.Runtime, copied.Model, copied.Effort, copied.Instruction}, p.Name)
	}

	assert.Equal(t, map[string]string{"explorer": "claude", "implementer": "claude", "reviewer": "claude"}, names,
		"pi on the server is a Bot, not a profile")

	implementer, err := w.s.GetProfile(ctx, "implementer")
	require.NoError(t, err)

	again, created, err := w.s.ApplyProfile(ctx, "implementer", implementer.Source)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, implementer.VersionID, again.VersionID)
}

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

	v2, created, err := w.s.ApplyProfile(ctx, "", source)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 6, v2.Version, "after the four seeded and the world's own")
	assert.Equal(t, "model-gateway", v2.Provider)

	same, created, err := w.s.ApplyProfile(ctx, "", source)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, v2.VersionID, same.VersionID)

	versions, err := w.s.ListProfileVersions(ctx, "implementer")
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

// A profile is checked when it is applied: its name, fields, runtime,
// effort, and whether its provider offers the model to that runtime. A
// Claude Code profile must name the model gateway or a subscription it
// serves (issues #381, #549).
func TestAProfileIsCheckedWhenApplied(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	_, err := w.s.ApplyProvider(t.Context(), "", "name: router\nkind: openai_compatible\n"+
		"base_url: https://router.example/v1\napi_key_ref: ROUTER_KEY\nmodels: [claude-sonnet-5-5, gpt-5]\n")
	require.NoError(t, err)

	for source, want := range map[string]string{
		"name: Bad Name\ndescription: d\nruntime: claude\n":                                        "lowercase",
		"name: x\ndescription: d\nruntime: claude\nflavor: red\n":                                  "flavor",
		"name: x\ndescription: d\nruntime: gemini\n":                                               "unknown runtime",
		"name: x\ndescription: d\nruntime: claude\nmodel: {name: claude-opus-5-5, effort: huge}\n": "unknown effort",
		"name: x\ndescription: d\nruntime: claude\nmodel: {name: gpt-5}\n":                         "no provider",
		"name: x\ndescription: d\nruntime: claude\nmodel: {provider: router, name: opus}\n":        "must name a subscription or a provider of kind model_gateway",
		"name: x\ndescription: d\nruntime: claude\nmodel: {name: claude-mythos}\n":                 "no provider of kind model_gateway offers",
		"name: x\ndescription: d\nruntime: omp\nmodel: {provider: router, name: gpt-5}\n":          "cannot run against",
		"name: x\ndescription: d\nruntime: codex\nmodel: {provider: router, name: opus}\n":         "does not offer",
		"name: x\ndescription: d\nruntime: pi\n":                                                   "a bot's",
		"name: x\ndescription: d\nruntime: codex\nmodel: {provider: nobody}\n":                     "not found",
		"name: x\nruntime: claude\n":                                                               "needs a description",
	} {
		_, _, err := w.s.ApplyProfile(t.Context(), "", source)
		require.ErrorContains(t, err, want, source)
	}

	_, _, err = w.s.ApplyProfile(t.Context(), "other", "name: x\ndescription: d\nruntime: claude\n")
	require.ErrorIs(t, err, ErrInvalid, "the name in the path must be the source's")

	// Every model is named with its provider (issue #1002), even one a single provider offers.
	_, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {name: gpt-5}\n")
	require.ErrorContains(t, err, `model "gpt-5" is named without its provider; name it <provider>/<model>, `+
		"such as router/gpt-5")

	p, _, err := w.s.ApplyProfile(t.Context(), "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {provider: router, name: gpt-5}\n")
	require.NoError(t, err)
	assert.Equal(t, "router", p.Provider)

	p, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: x\ndescription: d\nruntime: claude\nmodel: {provider: claude-subscription, name: claude-haiku-5-5}\n")
	require.NoError(t, err)
	assert.Equal(t, "claude-subscription", p.Provider)

	p, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: v\ndescription: d\nruntime: claude\nmodel: {provider: claude-subscription}\n")
	require.NoError(t, err, "a subscription is reached through the gateway (issue #549)")
	assert.Equal(t, "claude-subscription", p.Provider)

	_, err = w.s.ApplyProvider(t.Context(), "", "name: anthropic\nkind: anthropic\nmodels: [claude-mythos]\n")
	require.NoError(t, err)

	_, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: y\ndescription: d\nruntime: claude\nmodel: {name: claude-mythos}\n")
	require.ErrorContains(t, err, "no provider of kind model_gateway offers", "nor an API of Anthropic's")

	_, _, err = w.s.ApplyProfile(t.Context(), "",
		"name: w\ndescription: d\nruntime: claude\nmodel: {name: claude-sonnet-5-5}\n")
	require.ErrorContains(t, err, "such as claude-subscription/claude-sonnet-5-5 or model-gateway/claude-sonnet-5-5",
		"the hint names each provider claude reaches claude-sonnet-5-5 through")
}

// A provider names where its key is kept, never the key; and it cannot drop
// a model or a kind a profile depends on.
func TestAProviderKeepsItsProfilesWorking(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\napi_key_ref: sk-live-123\n")
	require.ErrorIs(t, err, ErrInvalid, "a pasted key is refused")

	router := "name: router\nkind: openai_compatible\napi_key_ref: ROUTER_KEY\nmodels: [m1, m2]\n"
	p, err := w.s.ApplyProvider(ctx, "router", router)
	require.NoError(t, err)
	assert.Equal(t, []string{"m1", "m2"}, p.Models)

	_, err = w.s.PutBot(ctx, Bot{Name: DefaultBotID, Provider: "router", Model: "m1"})
	require.NoError(t, err)

	_, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\nmodels: [m2]\n")
	require.ErrorIs(t, err, ErrConflict, "the bot coordinator uses m1")

	_, _, err = w.s.ApplyProfile(ctx, "", "name: z\ndescription: d\nruntime: codex\nmodel: {provider: router, name: m2}\n")
	require.NoError(t, err)

	_, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: anthropic\nmodels: [m1, m2]\n")
	require.ErrorIs(t, err, ErrConflict, "codex cannot run against an API of Anthropic's")

	p, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: codex_subscription\nmodels: [m1, m2, m3]\n")
	require.NoError(t, err, "pi and codex both reach a codex subscription through the gateway")
	assert.Equal(t, []string{"m1", "m2", "m3"}, p.Models, "the models are replaced")

	models, err := w.s.ListModels(ctx)
	require.NoError(t, err)
	assert.Contains(t, models, Model{Provider: "router", Name: "m3", Kind: ProviderCodexSubscription})
}

// Scenario: the owner removes a provider (issue #610). One a bot, a profile version or a gateway model's target
// still names is kept, with what uses it in the error; one nothing names goes with its models.
func TestAProviderIsRemovedWhenNothingNamesIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	require.ErrorIs(t, w.s.DeleteProvider(ctx, "nobody"), ErrNotFound)

	_, err := w.s.ApplyProvider(ctx, "", "name: spare\nkind: openai_compatible\napi_key_ref: K\nmodels: [m1, m2]\n")
	require.NoError(t, err)

	// A profile that names it keeps it.
	_, _, err = w.s.ApplyProfile(ctx, "", "name: z\ndescription: d\nruntime: codex\nmodel: {provider: spare, name: m1}\n")
	require.NoError(t, err)
	err = w.s.DeleteProvider(ctx, "spare")
	require.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, err.Error(), "profile version")

	// Once nothing names it, it and its models go.
	require.NoError(t, w.s.DeleteProfile(ctx, "z"))
	require.NoError(t, w.s.DeleteProvider(ctx, "spare"))

	_, err = w.s.GetProvider(ctx, "spare")
	require.ErrorIs(t, err, ErrNotFound)

	models, err := w.s.ListModels(ctx)
	require.NoError(t, err)
	for _, m := range models {
		assert.NotEqual(t, "spare", m.Provider, "its models went too")
	}
}

// A Task is checked against its profile when it is created; one that names
// none runs as an implementer.
func TestATaskIsCheckedAgainstItsProfile(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	created := w.tasks(NewTask{Ref: "a", Spec: "a"})
	assert.Equal(t, DefaultProfile, created[0].Profile)

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
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 69)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
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

	claude, err := s.GetProvider(ctx, "claude-subscription")
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

	claude, err = s.GetProvider(ctx, "claude-subscription")
	require.NoError(t, err)
	assert.Subset(t, claude.Models, []string{"opus", "sonnet", "haiku", "fable"})
	assert.Contains(t, claude.Source,
		"models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, opus, sonnet, haiku]\n")
}

// Scenario (issue #623): Codex now serves gpt-6.1-terra, which the codex-subscription provider's list lacks. It is
// added, once, after the models already there; a model Codex no longer names stays, and the source says what was
// stored, so applying it again changes nothing.
func TestAProviderIsGivenTheModelsItsVendorServes(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: cx\nkind: codex_subscription\nmodels: [gpt-6.1-sol, gpt-5.6-luna]\n")
	require.NoError(t, err)

	added, err := w.s.AddProviderModels(ctx, "cx", []string{"gpt-6.1-sol", "gpt-6.1-terra"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-6.1-terra"}, added)

	p, err := w.s.GetProvider(ctx, "cx")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gpt-6.1-sol", "gpt-5.6-luna", "gpt-6.1-terra"}, p.Models,
		"gpt-5.6-luna, which the vendor no longer named, stays")
	assert.Contains(t, p.Source, "models: [gpt-6.1-sol, gpt-5.6-luna, gpt-6.1-terra]")

	added, err = w.s.AddProviderModels(ctx, "cx", []string{"gpt-6.1-terra"})
	require.NoError(t, err)
	assert.Empty(t, added, "nothing is added twice")

	_, err = w.s.AddProviderModels(ctx, "nobody", []string{"m"})
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario (issue #639): Antigravity's roster no longer lists gemini-2.5-pro and now lists gemini-3.8-flash-high.
// The provider's models become the roster: the kept ones in their order, the new one after, the dropped one gone.
// A model a profile uses is not dropped: the change is refused and the provider stays as it was.
func TestAProviderIsSetToItsVendorsRoster(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "",
		"name: ag\nkind: antigravity_subscription\nmodels: [gemini-3-flash, gemini-2.5-pro, claude-sonnet-4-6]\n")
	require.NoError(t, err)

	added, removed, err := w.s.SetProviderModels(ctx, "ag",
		[]string{"claude-sonnet-4-6", "gemini-3-flash", "gemini-3.8-flash-high"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-3.8-flash-high"}, added)
	assert.Equal(t, []string{"gemini-2.5-pro"}, removed)

	p, err := w.s.GetProvider(ctx, "ag")
	require.NoError(t, err)
	assert.Contains(t, p.Source, "models: [gemini-3-flash, claude-sonnet-4-6, gemini-3.8-flash-high]")

	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: z\ndescription: d\nruntime: claude\nmodel: {provider: ag, name: gemini-3-flash}\n")
	require.NoError(t, err)

	_, _, err = w.s.SetProviderModels(ctx, "ag", []string{"claude-sonnet-4-6"})
	require.ErrorIs(t, err, ErrConflict, "a model a profile uses stays")

	p, err = w.s.GetProvider(ctx, "ag")
	require.NoError(t, err)
	assert.Contains(t, p.Models, "gemini-3-flash")
}

// Scenario (issue #814): one model a Codex provider lists is taken off, and nothing else on the list changes, even a
// model below the provider's floor listed by hand. A model a profile uses is not taken off.
func TestOneModelIsTakenOffAProvider(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: cx\nkind: codex_subscription\nmin_versions: {gpt: \"6\"}\n"+
		"models: [gpt-6.1-sol, gpt-5.6-luna, gpt-6.1-gone]\n")
	require.NoError(t, err)

	removed, err := w.s.RemoveProviderModel(ctx, "cx", "gpt-6.1-gone")
	require.NoError(t, err)
	assert.True(t, removed)

	p, err := w.s.GetProvider(ctx, "cx")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gpt-6.1-sol", "gpt-5.6-luna"}, p.Models)

	removed, err = w.s.RemoveProviderModel(ctx, "cx", "gpt-6.1-gone")
	require.NoError(t, err)
	assert.False(t, removed, "no longer listed")

	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {provider: cx, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	_, err = w.s.RemoveProviderModel(ctx, "cx", "gpt-6.1-sol")
	require.ErrorIs(t, err, ErrConflict, "a model a profile uses stays")
}

// Scenario (issue #642): the owner keeps only the current lines, Codex 6 and up and Gemini 3.8 and up. Codex's list
// adds gpt-6.2-sol and not gpt-5.6-mini; Antigravity's roster brings gemini-4-pro and gemini-3.8-flash-low, not
// gemini-2.5-flash or gemini-3-flash, and the gemini-2.5-pro listed before goes; gpt-oss-120b, with no version to
// read, stays. A model the owner lists by hand below the line stays listed.
func TestAProviderAddsOnlyTheVersionsItsOwnerKeeps(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: cx\nkind: codex_subscription\nmin_versions: {gpt: \"6\"}\n"+
		"models: [gpt-6.1-sol, gpt-5.6-luna]\n")
	require.NoError(t, err)

	added, err := w.s.AddProviderModels(ctx, "cx", []string{"gpt-6.2-sol", "gpt-5.6-mini", "gpt-6-luna"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-6.2-sol", "gpt-6-luna"}, added)

	p, err := w.s.GetProvider(ctx, "cx")
	require.NoError(t, err)
	assert.Contains(t, p.Models, "gpt-5.6-luna", "what the owner listed stays")

	_, err = w.s.ApplyProvider(ctx, "", "name: ag\nkind: antigravity_subscription\nmin_versions: {gemini: \"3.8\"}\n"+
		"models: [gemini-3.8-flash-high, gemini-2.5-pro]\n")
	require.NoError(t, err)

	added, removed, err := w.s.SetProviderModels(ctx, "ag", []string{
		"gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-4-pro", "gemini-2.5-flash", "gemini-3-flash",
		"gpt-oss-120b-medium",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-3.8-flash-low", "gemini-4-pro", "gpt-oss-120b-medium"}, added)
	assert.Equal(t, []string{"gemini-2.5-pro"}, removed)

	// Anthropic writes a version as two numbers and may end the name with a date.
	_, err = w.s.ApplyProvider(ctx, "", "name: cl\nkind: claude_subscription\nmin_versions: {claude: \"5\"}\n"+
		"models: [claude-opus-5-5]\n")
	require.NoError(t, err)

	added, err = w.s.AddProviderModels(ctx, "cl", []string{
		"claude-haiku-4-5-20251001", "claude-opus-4-8", "claude-fable-5", "claude-sonnet-5-5", "claude-opus-6",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-fable-5", "claude-sonnet-5-5", "claude-opus-6"}, added)

	_, err = w.s.ApplyProvider(ctx, "", "name: bad\nkind: codex_subscription\nmin_versions: {gpt: six}\n")
	require.ErrorIs(t, err, ErrInvalid)
}

// Scenario (review of PR #1000, rounds 1 and 2): two versions from before issue #1002 have the same YAML, naming
// their model without a provider, and recorded two providers for it. The owner restores the first: it runs under
// the provider the first recorded, not the later one's. Applying that YAML as a new source is still refused.
func TestARestoredVersionKeepsTheProviderItRecorded(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.AddProviderModels(ctx, "opencode-go", []string{"claude-haiku-5-5"})
	require.NoError(t, err)

	apply := func(provider, model string) Profile {
		p, _, err := w.s.ApplyProfile(ctx, "", "name: x\ndescription: d\nruntime: claude\n"+
			"model: {provider: "+provider+", name: "+model+"}\n")
		require.NoError(t, err)

		return p
	}

	// As main stored them: the YAML as written, the provider checkModel found then.
	bare := "name: x\ndescription: d\nruntime: claude\nmodel: {name: claude-haiku-5-5}\n"
	asWritten := func(p Profile) {
		_, err := w.db.ExecContext(ctx, `UPDATE profile_versions SET source = ?, digest = ? WHERE id = ?`,
			bare, sourceDigest(bare), p.VersionID)
		require.NoError(t, err)
	}

	first := apply("claude-subscription", "claude-haiku-5-5")
	asWritten(first)
	apply("claude-subscription", "claude-opus-5-5")
	second := apply("opencode-go", "claude-haiku-5-5")
	asWritten(second)
	apply("claude-subscription", "claude-opus-5-5")

	restored, created, err := w.s.RestoreProfileVersion(ctx, "", "x", first.VersionID)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, [2]string{"claude-subscription", "claude-haiku-5-5"}, [2]string{restored.Provider, restored.Model})

	restored, _, err = w.s.RestoreProfileVersion(ctx, "", "x", second.VersionID)
	require.NoError(t, err)
	assert.Equal(t, "opencode-go", restored.Provider)

	_, _, err = w.s.RestoreProfileVersion(ctx, "", "other", first.VersionID)
	require.ErrorIs(t, err, ErrNotFound, "a version is restored under its own profile")

	_, _, err = w.s.ApplyProfile(ctx, "", "name: x\ndescription: other\nruntime: claude\n"+
		"model: {name: claude-haiku-5-5}\n")
	require.ErrorContains(t, err, "named without its provider")
}

// Scenario (review of PR #1000, round 3): a version from before migration 00123, whose YAML still writes the old
// allowlist of web tools only, as 00123 left it beside the policy it re-expressed (every other family off). Restoring
// it keeps node_exec off, as the page shows the version, and never widens the policy to the old YAML read anew.
func TestARestoredVersionKeepsItsMigratedToolPolicy(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	old, _, err := w.s.ApplyProfile(ctx, "", "name: x\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription, name: claude-haiku-5-5}\npreset: full\ntools_off: [node_*]\n")
	require.NoError(t, err)

	// As 00123 left it: the YAML as written then, the policy re-expressed in the columns.
	written := "name: x\ndescription: d\nruntime: claude\n" +
		"model: {provider: claude-subscription, name: claude-haiku-5-5}\ntools: [web_search, web_fetch]\n"
	_, err = w.db.ExecContext(ctx, `UPDATE profile_versions SET source = ?, digest = ? WHERE id = ?`,
		written, sourceDigest(written), old.VersionID)
	require.NoError(t, err)

	_, _, err = w.s.ApplyProfile(ctx, "", "name: x\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription, name: claude-opus-5-5}\n")
	require.NoError(t, err)

	restored, _, err := w.s.RestoreProfileVersion(ctx, "", "x", old.VersionID)
	require.NoError(t, err)
	assert.Equal(t, "full", restored.Preset)
	assert.Equal(t, []string{"node_*"}, restored.ToolsOff, "node_exec stays off")
	assert.Equal(t, "claude-haiku-5-5", restored.Model)
}
