package profilestore_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// newSeedStore is the store over a database whose providers and profiles are the starting set as the migrations seed
// it, the model gateway's included (issue #462).
func newSeedStore(t *testing.T) *profilestore.Store {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	n := 0
	ids := func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}

	return profilestore.New(db, &fakeClock{now: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}, ids, func() {})
}

// newStore is a seeded store with a model gateway of its own, whose implementer runs on it.
func newStore(t *testing.T) *profilestore.Store {
	t.Helper()

	s := newSeedStore(t)

	_, err := s.ApplyProvider(t.Context(), "",
		"name: model-gateway\nkind: model_gateway\nmodels: [claude-opus-5-5, claude-sonnet-5-5]\n")
	require.NoError(t, err)

	_, _, err = s.ApplyProfile(t.Context(), "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway}\n")
	require.NoError(t, err)

	return s
}

// Scenario: a fresh control plane has the starting set, and applying a
// seeded profile's own source changes nothing.
func TestTheStartingSetIsThere(t *testing.T) {
	t.Parallel()

	s := newSeedStore(t)
	ctx := t.Context()

	profiles, err := s.ListProfiles(ctx)
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
		same, created, err := s.ApplyProfile(ctx, p.Name, p.Source)
		require.NoError(t, err, p.Name)
		assert.False(t, created, "%s: the digest is the source's", p.Name)
		assert.Equal(t, p.VersionID, same.VersionID, p.Name)

		copied, _, err := s.ApplyProfile(ctx, "",
			strings.Replace(p.Source, "name: "+p.Name+"\n", "name: copy-"+p.Name+"\n", 1))
		require.NoError(t, err, p.Name)
		assert.Equal(t, []string{p.Description, p.Runtime, p.Model, p.Effort, p.Instructions},
			[]string{copied.Description, copied.Runtime, copied.Model, copied.Effort, copied.Instructions}, p.Name)
	}

	assert.Equal(t, map[string]string{"explorer": "claude", "implementer": "claude", "reviewer": "claude"}, names,
		"pi on the server is a Bot, not a profile")

	implementer, err := s.GetProfile(ctx, "implementer")
	require.NoError(t, err)

	again, created, err := s.ApplyProfile(ctx, "implementer", implementer.Source)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, implementer.VersionID, again.VersionID)
}

// A profile is checked when it is applied: its name, fields, runtime,
// effort, and whether its provider offers the model to that runtime. A
// Claude Code profile must name the model gateway or a subscription it
// serves (issues #381, #549).
func TestAProfileIsCheckedWhenApplied(t *testing.T) {
	t.Parallel()

	s := newStore(t)

	_, err := s.ApplyProvider(t.Context(), "", "name: router\nkind: openai_compatible\n"+
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
		_, _, err := s.ApplyProfile(t.Context(), "", source)
		require.ErrorContains(t, err, want, source)
	}

	_, _, err = s.ApplyProfile(t.Context(), "other", "name: x\ndescription: d\nruntime: claude\n")
	require.ErrorIs(t, err, storeerr.ErrInvalid, "the name in the path must be the source's")

	// Every model is named with its provider (issue #1002), even one a single provider offers.
	_, _, err = s.ApplyProfile(t.Context(), "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {name: gpt-5}\n")
	require.ErrorContains(t, err, `model "gpt-5" is named without its provider; name it <provider>/<model>, `+
		"such as router/gpt-5")

	p, _, err := s.ApplyProfile(t.Context(), "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {provider: router, name: gpt-5}\n")
	require.NoError(t, err)
	assert.Equal(t, "router", p.Provider)

	p, _, err = s.ApplyProfile(t.Context(), "",
		"name: x\ndescription: d\nruntime: claude\nmodel: {provider: claude-subscription, name: claude-haiku-5-5}\n")
	require.NoError(t, err)
	assert.Equal(t, "claude-subscription", p.Provider)

	p, _, err = s.ApplyProfile(t.Context(), "",
		"name: v\ndescription: d\nruntime: claude\nmodel: {provider: claude-subscription}\n")
	require.NoError(t, err, "a subscription is reached through the gateway (issue #549)")
	assert.Equal(t, "claude-subscription", p.Provider)

	_, err = s.ApplyProvider(t.Context(), "", "name: anthropic\nkind: anthropic\nmodels: [claude-mythos]\n")
	require.NoError(t, err)

	_, _, err = s.ApplyProfile(t.Context(), "",
		"name: y\ndescription: d\nruntime: claude\nmodel: {name: claude-mythos}\n")
	require.ErrorContains(t, err, "no provider of kind model_gateway offers", "nor an API of Anthropic's")

	_, _, err = s.ApplyProfile(t.Context(), "",
		"name: w\ndescription: d\nruntime: claude\nmodel: {name: claude-sonnet-5-5}\n")
	require.ErrorContains(t, err, "such as claude-subscription/claude-sonnet-5-5 or model-gateway/claude-sonnet-5-5",
		"the hint names each provider claude reaches claude-sonnet-5-5 through")
}

// Scenario: the owner removes a provider (issue #610). One a bot, a profile version or a gateway model's target
// still names is kept, with what uses it in the error; one nothing names goes with its models.
func TestAProviderIsRemovedWhenNothingNamesIt(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	ctx := t.Context()

	require.ErrorIs(t, s.DeleteProvider(ctx, "nobody"), storeerr.ErrNotFound)

	_, err := s.ApplyProvider(ctx, "", "name: spare\nkind: openai_compatible\napi_key_ref: K\nmodels: [m1, m2]\n")
	require.NoError(t, err)

	// A profile that names it keeps it.
	_, _, err = s.ApplyProfile(ctx, "", "name: z\ndescription: d\nruntime: codex\nmodel: {provider: spare, name: m1}\n")
	require.NoError(t, err)
	err = s.DeleteProvider(ctx, "spare")
	require.ErrorIs(t, err, storeerr.ErrConflict)
	assert.Contains(t, err.Error(), "profile version")

	// Once nothing names it, it and its models go.
	require.NoError(t, s.DeleteProfile(ctx, "z"))
	require.NoError(t, s.DeleteProvider(ctx, "spare"))

	_, err = s.GetProvider(ctx, "spare")
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	models, err := s.ListModels(ctx)
	require.NoError(t, err)
	for _, m := range models {
		assert.NotEqual(t, "spare", m.Provider, "its models went too")
	}
}

// Scenario (issue #623): Codex now serves gpt-6.1-terra, which the codex-subscription provider's list lacks. It is
// added, once, after the models already there; a model Codex no longer names stays, and the source says what was
// stored, so applying it again changes nothing.
func TestAProviderIsGivenTheModelsItsVendorServes(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	ctx := t.Context()

	_, err := s.ApplyProvider(ctx, "", "name: cx\nkind: codex_subscription\nmodels: [gpt-6.1-sol, gpt-5.6-luna]\n")
	require.NoError(t, err)

	added, err := s.AddProviderModels(ctx, "cx", []string{"gpt-6.1-sol", "gpt-6.1-terra"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-6.1-terra"}, added)

	p, err := s.GetProvider(ctx, "cx")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gpt-6.1-sol", "gpt-5.6-luna", "gpt-6.1-terra"}, p.Models,
		"gpt-5.6-luna, which the vendor no longer named, stays")
	assert.Contains(t, p.Source, "models: [gpt-6.1-sol, gpt-5.6-luna, gpt-6.1-terra]")

	added, err = s.AddProviderModels(ctx, "cx", []string{"gpt-6.1-terra"})
	require.NoError(t, err)
	assert.Empty(t, added, "nothing is added twice")

	_, err = s.AddProviderModels(ctx, "nobody", []string{"m"})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

// Scenario (issue #639): Antigravity's roster no longer lists gemini-2.5-pro and now lists gemini-3.8-flash-high.
// The provider's models become the roster: the kept ones in their order, the new one after, the dropped one gone.
// A model a profile uses is not dropped: the change is refused and the provider stays as it was.
func TestAProviderIsSetToItsVendorsRoster(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	ctx := t.Context()

	_, err := s.ApplyProvider(ctx, "",
		"name: ag\nkind: antigravity_subscription\nmodels: [gemini-3-flash, gemini-2.5-pro, claude-sonnet-4-6]\n")
	require.NoError(t, err)

	added, removed, err := s.SetProviderModels(ctx, "ag",
		[]string{"claude-sonnet-4-6", "gemini-3-flash", "gemini-3.8-flash-high"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-3.8-flash-high"}, added)
	assert.Equal(t, []string{"gemini-2.5-pro"}, removed)

	p, err := s.GetProvider(ctx, "ag")
	require.NoError(t, err)
	assert.Contains(t, p.Source, "models: [gemini-3-flash, claude-sonnet-4-6, gemini-3.8-flash-high]")

	_, _, err = s.ApplyProfile(ctx, "",
		"name: z\ndescription: d\nruntime: claude\nmodel: {provider: ag, name: gemini-3-flash}\n")
	require.NoError(t, err)

	_, _, err = s.SetProviderModels(ctx, "ag", []string{"claude-sonnet-4-6"})
	require.ErrorIs(t, err, storeerr.ErrConflict, "a model a profile uses stays")

	p, err = s.GetProvider(ctx, "ag")
	require.NoError(t, err)
	assert.Contains(t, p.Models, "gemini-3-flash")
}

// Scenario (issue #814): one model a Codex provider lists is taken off, and nothing else on the list changes, even a
// model below the provider's floor listed by hand. A model a profile uses is not taken off.
func TestOneModelIsTakenOffAProvider(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	ctx := t.Context()

	_, err := s.ApplyProvider(ctx, "", "name: cx\nkind: codex_subscription\nmin_versions: {gpt: \"6\"}\n"+
		"models: [gpt-6.1-sol, gpt-5.6-luna, gpt-6.1-gone]\n")
	require.NoError(t, err)

	removed, err := s.RemoveProviderModel(ctx, "cx", "gpt-6.1-gone")
	require.NoError(t, err)
	assert.True(t, removed)

	p, err := s.GetProvider(ctx, "cx")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gpt-6.1-sol", "gpt-5.6-luna"}, p.Models)

	removed, err = s.RemoveProviderModel(ctx, "cx", "gpt-6.1-gone")
	require.NoError(t, err)
	assert.False(t, removed, "no longer listed")

	_, _, err = s.ApplyProfile(ctx, "",
		"name: z\ndescription: d\nruntime: codex\nmodel: {provider: cx, name: gpt-6.1-sol}\n")
	require.NoError(t, err)

	_, err = s.RemoveProviderModel(ctx, "cx", "gpt-6.1-sol")
	require.ErrorIs(t, err, storeerr.ErrConflict, "a model a profile uses stays")
}

// Scenario (issue #642): the owner keeps only the current lines, Codex 6 and up and Gemini 3.8 and up. Codex's list
// adds gpt-6.2-sol and not gpt-5.6-mini; Antigravity's roster brings gemini-4-pro and gemini-3.8-flash-low, not
// gemini-2.5-flash or gemini-3-flash, and the gemini-2.5-pro listed before goes; gpt-oss-120b, with no version to
// read, stays. A model the owner lists by hand below the line stays listed.
func TestAProviderAddsOnlyTheVersionsItsOwnerKeeps(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	ctx := t.Context()

	_, err := s.ApplyProvider(ctx, "", "name: cx\nkind: codex_subscription\nmin_versions: {gpt: \"6\"}\n"+
		"models: [gpt-6.1-sol, gpt-5.6-luna]\n")
	require.NoError(t, err)

	added, err := s.AddProviderModels(ctx, "cx", []string{"gpt-6.2-sol", "gpt-5.6-mini", "gpt-6-luna"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-6.2-sol", "gpt-6-luna"}, added)

	p, err := s.GetProvider(ctx, "cx")
	require.NoError(t, err)
	assert.Contains(t, p.Models, "gpt-5.6-luna", "what the owner listed stays")

	_, err = s.ApplyProvider(ctx, "", "name: ag\nkind: antigravity_subscription\nmin_versions: {gemini: \"3.8\"}\n"+
		"models: [gemini-3.8-flash-high, gemini-2.5-pro]\n")
	require.NoError(t, err)

	added, removed, err := s.SetProviderModels(ctx, "ag", []string{
		"gemini-3.8-flash-high", "gemini-3.8-flash-low", "gemini-4-pro", "gemini-2.5-flash", "gemini-3-flash",
		"gpt-oss-120b-medium",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-3.8-flash-low", "gemini-4-pro", "gpt-oss-120b-medium"}, added)
	assert.Equal(t, []string{"gemini-2.5-pro"}, removed)

	// Anthropic writes a version as two numbers and may end the name with a date.
	_, err = s.ApplyProvider(ctx, "", "name: cl\nkind: claude_subscription\nmin_versions: {claude: \"5\"}\n"+
		"models: [claude-opus-5-5]\n")
	require.NoError(t, err)

	added, err = s.AddProviderModels(ctx, "cl", []string{
		"claude-haiku-4-5-20251001", "claude-opus-4-8", "claude-fable-5", "claude-sonnet-5-5", "claude-opus-6",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-fable-5", "claude-sonnet-5-5", "claude-opus-6"}, added)

	_, err = s.ApplyProvider(ctx, "", "name: bad\nkind: codex_subscription\nmin_versions: {gpt: six}\n")
	require.ErrorIs(t, err, storeerr.ErrInvalid)
}

// Review of PR #1267, round 1: a document that cannot be read is refused with the YAML decoder's own text, which
// the client API returns as it is. Moving the types into this package kept that text: each still names the type as
// orchestration named it, as main's errors were read on 4e533fce.
func TestAnUnreadableDocumentIsRefusedWithTheSameText(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	ctx := t.Context()

	const prefix = "invalid request: not a valid document: yaml: unmarshal errors:\n  "

	profile := func(source string) error {
		_, _, err := s.ApplyProfile(ctx, "", source)

		return err
	}
	provider := func(source string) error {
		_, err := s.ApplyProvider(ctx, "", source)

		return err
	}

	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"profile field": {
			profile("name: x\ndescription: d\nruntime: claude\nflavor: red\n"),
			"line 4: field flavor not found in type orchestration.profileSource",
		},
		"profile kind": {profile("- a\n"), "line 1: cannot unmarshal !!seq into orchestration.profileSource"},
		// What the document says is echoed as written, even a field named as one of the types (review round 2).
		"profile field named as a type": {
			profile("name: x\ndescription: d\nruntime: claude\nprofilestore.Source: red\n"),
			"line 4: field profilestore.Source not found in type orchestration.profileSource",
		},
		"profile field with the decoder's words": {
			profile("name: x\ndescription: d\nruntime: claude\na into profilestore.RoleEntry: red\n"),
			"line 4: field a into profilestore.RoleEntry not found in type orchestration.profileSource",
		},
		"requests field": {
			profile("name: x\ndescription: d\nruntime: claude\nrequests: {gpu: 1}\n"),
			"line 4: field gpu not found in type orchestration.requestsSource",
		},
		"requests kind": {
			profile("name: x\ndescription: d\nruntime: claude\nrequests: [1]\n"),
			"line 4: cannot unmarshal !!seq into orchestration.requestsSource",
		},
		"provider field": {
			provider("name: p\nkind: anthropic\ncolor: red\n"),
			"line 3: field color not found in type orchestration.providerSource",
		},
		"provider kind": {provider("- a\n"), "line 1: cannot unmarshal !!seq into orchestration.providerSource"},
		"routing entry field": {
			s.ApplyRoleRouting(ctx, "implementer:\n  broad:\n    - {runtime: claude, color: red}\n"),
			"line 3: field color not found in type orchestration.roleEntry",
		},
		"routing entry kind": {
			s.ApplyRoleRouting(ctx, "implementer:\n  broad: [x]\n"),
			"line 2: cannot unmarshal !!str `x` into orchestration.roleEntry",
		},
		"routing kind": {
			s.ApplyRoleRouting(ctx, "- a\n"), "line 1: cannot unmarshal !!seq into orchestration.roleRouting",
		},
	} {
		require.ErrorIs(t, c.err, storeerr.ErrInvalid, name)
		assert.EqualError(t, c.err, prefix+c.want, name)
	}
}
