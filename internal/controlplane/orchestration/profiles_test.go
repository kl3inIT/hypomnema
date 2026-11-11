package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: a fresh control plane has the starting set, and applying a
// seeded profile's own source changes nothing.
func TestTheStartingSetIsThere(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	profiles, err := w.s.ListProfiles(ctx)
	require.NoError(t, err)

	// The seed, the seed with its description, and for the two that report in a set form, that form (issue #155).
	versions := map[string]int{"coordinator": 2, "explorer": 3, "implementer": 2, "reviewer": 3}

	names := map[string]string{}
	for _, p := range profiles {
		names[p.Name] = p.Runtime
		assert.NotEmpty(t, p.Description, "%s says what it is for", p.Name)
		assert.Equal(t, versions[p.Name], p.Version, p.Name)

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

	assert.Equal(t, map[string]string{
		"coordinator": "pi", "explorer": "claude", "implementer": "claude", "reviewer": "claude",
	}, names)

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

	source := "name: implementer\ndescription: d\nruntime: claude\nmodel:\n  name: sonnet\n  effort: high\n" +
		"instruction: Run the tests before finishing.\n"

	v2, created, err := w.s.ApplyProfile(ctx, "", source)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 3, v2.Version)
	assert.Equal(t, "claude-subscription", v2.Provider, "found from the model")

	same, created, err := w.s.ApplyProfile(ctx, "", source)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, v2.VersionID, same.VersionID)

	versions, err := w.s.ListProfileVersions(ctx, "implementer")
	require.NoError(t, err)
	require.Len(t, versions, 3)
	assert.Equal(t, 3, versions[0].Version, "newest first")

	w.tasks(NewTask{Ref: "b", Spec: "b"})
	second := w.dispatchOf(w.schedule().Created[0])

	assert.NotEqual(t, v2.VersionID, w.dispatchOf(first.ID).ProfileVersionID, "the first keeps its version")
	assert.Equal(t, v2.VersionID, second.ProfileVersionID)
	assert.Equal(t, "sonnet", second.Model)

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
// effort, and whether its provider offers the model to that runtime.
func TestAProfileIsCheckedWhenApplied(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	_, err := w.s.ApplyProvider(t.Context(), "", "name: router\nkind: openai_compatible\n"+
		"base_url: https://router.example/v1\napi_key_ref: ROUTER_KEY\nmodels: [sonnet, gpt-5]\n")
	require.NoError(t, err)

	for source, want := range map[string]string{
		"name: Bad Name\ndescription: d\nruntime: claude\n":                                 "lowercase",
		"name: x\ndescription: d\nruntime: claude\nflavor: red\n":                           "flavor",
		"name: x\ndescription: d\nruntime: gemini\n":                                        "unknown runtime",
		"name: x\ndescription: d\nruntime: claude\nmodel: {name: opus, effort: huge}\n":     "unknown effort",
		"name: x\ndescription: d\nruntime: claude\nmodel: {name: gpt-5}\n":                  "no provider",
		"name: x\ndescription: d\nruntime: claude\nmodel: {provider: router, name: opus}\n": "cannot run against",
		"name: x\ndescription: d\nruntime: pi\nmodel: {provider: router, name: opus}\n":     "does not offer",
		"name: x\ndescription: d\nruntime: codex\nmodel: {provider: nobody}\n":              "not found",
		"name: x\nruntime: claude\n":                                                        "needs a description",
	} {
		_, _, err := w.s.ApplyProfile(t.Context(), "", source)
		require.ErrorContains(t, err, want, source)
	}

	_, _, err = w.s.ApplyProfile(t.Context(), "other", "name: x\ndescription: d\nruntime: claude\n")
	require.ErrorIs(t, err, ErrInvalid, "the name in the path must be the source's")

	p, _, err := w.s.ApplyProfile(t.Context(), "",
		"name: coordinator\ndescription: d\nruntime: pi\nmodel: {name: sonnet}\n")
	require.NoError(t, err)
	assert.Equal(t, "router", p.Provider, "only router offers sonnet to pi")

	p, _, err = w.s.ApplyProfile(t.Context(), "", "name: x\ndescription: d\nruntime: claude\nmodel: {name: sonnet}\n")
	require.NoError(t, err)
	assert.Equal(t, "claude-subscription", p.Provider, "claude cannot use router's kind")

	_, err = w.s.ApplyProvider(t.Context(), "", "name: anthropic\nkind: anthropic\nmodels: [sonnet]\n")
	require.NoError(t, err)

	_, _, err = w.s.ApplyProfile(t.Context(), "", "name: y\ndescription: d\nruntime: claude\nmodel: {name: sonnet}\n")
	require.ErrorContains(t, err, "name the provider", "claude now reaches sonnet two ways")
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

	_, _, err = w.s.ApplyProfile(ctx, "",
		"name: coordinator\ndescription: d\nruntime: pi\nmodel: {provider: router, name: m1}\n")
	require.NoError(t, err)

	_, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\nmodels: [m2]\n")
	require.ErrorIs(t, err, ErrConflict, "the coordinator uses m1")

	_, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: codex_subscription\nmodels: [m1]\n")
	require.ErrorIs(t, err, ErrConflict, "pi cannot use a codex subscription")

	p, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: anthropic\nmodels: [m1, m3]\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"m1", "m3"}, p.Models, "the models are replaced")

	models, err := w.s.ListModels(ctx)
	require.NoError(t, err)
	assert.Contains(t, models, Model{Provider: "router", Name: "m3", Kind: ProviderAnthropic})
}

// A Task is checked against its profile when it is created; one that names
// none runs as an implementer.
func TestATaskIsCheckedAgainstItsProfile(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	created := w.tasks(NewTask{Ref: "a", Spec: "a"})
	assert.Equal(t, DefaultProfile, created[0].Profile)

	for nt, want := range map[*NewTask]string{
		{Ref: "b", Profile: "designer", Spec: "b"}:       "not found",
		{Ref: "c", Spec: "c", Model: "gpt-5"}:            "no provider",
		{Ref: "d", Spec: "d", Effort: "huge"}:            "unknown effort",
		{Ref: "e", Spec: "e", Runtime: "gemini"}:         "unknown runtime",
		{Ref: "f", Profile: "reviewer", Spec: "   "}:     "needs a spec",
		{Ref: "g", Spec: "g", Runtime: "pi", Model: "x"}: "no provider",
	} {
		_, err := w.s.CreateTasks(t.Context(), w.run.ID, []NewTask{*nt})
		require.ErrorContains(t, err, want, nt.Ref)
	}

	ok := w.tasks(NewTask{Ref: "h", Profile: "reviewer", Spec: "h", Model: "opus", Effort: "max"})
	assert.Equal(t, "reviewer", ok[0].Profile)
}
