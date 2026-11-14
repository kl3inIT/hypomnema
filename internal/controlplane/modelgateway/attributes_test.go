package modelgateway_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

func ptr[T any](v T) *T { return &v }

// Scenario: the owner serves the Codex subscription's model under a public
// name and sets nothing (issue #367). Its window is the one Codex itself
// gives the model, smaller than the API's that models.dev gives, past which
// the subscription refuses a request. Its largest output, inputs, reasoning
// and effort levels come from models.dev, with the effort levels
// the gateway gives Codex as levels of their own: max is sent as xhigh, so it
// is not one.
func TestAModelThatSetsNothingHasWhatModelsDevSays(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	m, err := g.ApplyModel(t.Context(), "name: sol\ntargets: [{provider: codex-subscription, model: gpt-6.1-sol}]\n")
	require.NoError(t, err)

	got, err := g.Resolve(t.Context(), m)
	require.NoError(t, err)

	seed, ok := prices.Find("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)

	assert.Equal(t, modelgateway.Value[int64]{Value: ptr(int64(272000)), Source: modelgateway.SourceCodex,
		ModelsDev: seed.Context}, got.ContextWindow)
	assert.Greater(t, *seed.Context, int64(272000), "models.dev gives the API's window")
	assert.Equal(t, modelgateway.SourceModelsDev, got.MaxOutput.Source)
	assert.Equal(t, seed.MaxOutput, got.MaxOutput.Value)
	assert.Equal(t, &[]string{"text", "image"}, got.Input.Value)
	assert.Equal(t, ptr(true), got.Reasoning.Value)
	require.NotNil(t, got.Efforts.Value)
	assert.Contains(t, seed.Efforts, "max")
	assert.NotContains(t, *got.Efforts.Value, "max", "the gateway sends Codex max as xhigh")
	assert.Contains(t, *got.Efforts.Value, "xhigh")
	require.NotNil(t, got.Price, "its price is the one its usage is counted at")
	assert.Equal(t, "gpt-6.1-sol", got.Price.Model)
}

// Scenario: the owner sets a smaller window and text only on the model, on
// the Models page: those win, models.dev's stay beside them, and what is not
// set still comes from models.dev. Taken out again, they come from models.dev.
// The rest of the YAML stays as it was.
func TestAValueSetOnTheModelWins(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	_, err := g.ApplyModel(t.Context(),
		"name: sol # the owner's\ntargets: [{provider: codex-subscription, model: gpt-6.1-sol}]\n")
	require.NoError(t, err)

	m, err := g.SetAttributes(t.Context(), "sol", modelgateway.Attributes{
		ContextWindow: ptr[int64](200_000), Input: []string{"text"}, Efforts: []string{"low", "high"},
	})
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.Target{{Provider: "codex-subscription", UpstreamModel: "gpt-6.1-sol"}}, m.Targets)
	assert.Contains(t, m.Source, "# the owner's")
	assert.Contains(t, m.Source, "context_window: 200000")

	got, err := g.Resolve(t.Context(), m)
	require.NoError(t, err)

	seed, _ := prices.Find("codex_subscription", "gpt-6.1-sol")
	assert.Equal(t, modelgateway.Value[int64]{Value: ptr[int64](200_000), Source: modelgateway.SourceSet,
		ModelsDev: seed.Context}, got.ContextWindow)
	assert.Equal(t, modelgateway.Value[[]string]{Value: &[]string{"text"}, Source: modelgateway.SourceSet,
		ModelsDev: &[]string{"text", "image"}}, got.Input)
	assert.Equal(t, &[]string{"low", "high"}, got.Efforts.Value)
	assert.Equal(t, modelgateway.SourceModelsDev, got.MaxOutput.Source, "what is not set comes from models.dev")

	listed, err := g.ListModels(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, ptr[int64](200_000), listed[0].Attributes.ContextWindow, "the YAML is the one home")

	m, err = g.SetAttributes(t.Context(), "sol", modelgateway.Attributes{})
	require.NoError(t, err)
	assert.NotContains(t, m.Source, "context_window")

	got, err = g.Resolve(t.Context(), m)
	require.NoError(t, err)
	assert.Equal(t, modelgateway.SourceCodex, got.ContextWindow.Source, "taken out, it is Codex's again")

	_, err = g.SetAttributes(t.Context(), "missing", modelgateway.Attributes{})
	require.ErrorIs(t, err, modelgateway.ErrNotFound)
}

// Scenario: a model models.dev does not know, served by an OpenAI-compatible
// provider, has its attributes unknown and no price; one set in its YAML is
// known. A value it cannot have, or an effort level the first target's
// provider does not take, is refused.
func TestAModelModelsDevDoesNotKnowIsUnknown(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	m, err := g.ApplyModel(t.Context(), "name: small\ntargets: [{provider: router, model: small}]\n")
	require.NoError(t, err)

	got, err := g.Resolve(t.Context(), m)
	require.NoError(t, err)
	assert.Equal(t, modelgateway.Resolved{
		ContextWindow: modelgateway.Value[int64]{Source: modelgateway.SourceUnknown},
		MaxOutput:     modelgateway.Value[int64]{Source: modelgateway.SourceUnknown},
		Input:         modelgateway.Value[[]string]{Source: modelgateway.SourceUnknown},
		Reasoning:     modelgateway.Value[bool]{Source: modelgateway.SourceUnknown},
		Efforts:       modelgateway.Value[[]string]{Source: modelgateway.SourceUnknown},
	}, got)

	m, err = g.ApplyModel(t.Context(), "name: small\ntargets: [{provider: router, model: small}]\n"+
		"context_window: 32768\nreasoning: true\nefforts: [low, medium, high, max]\n")
	require.NoError(t, err)

	got, err = g.Resolve(t.Context(), m)
	require.NoError(t, err)
	assert.Equal(t, modelgateway.Value[int64]{Value: ptr[int64](32768), Source: modelgateway.SourceSet},
		got.ContextWindow)
	assert.Equal(t, modelgateway.Value[bool]{Value: ptr(true), Source: modelgateway.SourceSet}, got.Reasoning)

	for name, attrs := range map[string]string{
		"a window of none":              "context_window: 0\n",
		"an input the gateway lacks":    "input: [text, audio]\n",
		"an effort that is not one":     "efforts: [turbo]\n",
		"an effort Claude is not given": "efforts: [minimal]\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := g.ApplyModel(t.Context(),
				"name: m\ntargets: [{provider: claude-subscription, model: claude-sonnet-5-5}]\n"+attrs)
			require.ErrorIs(t, err, modelgateway.ErrInvalid)
		})
	}
}

// Scenario: the owner serves Antigravity's claude-sonnet-4-6 as
// claude-sonnet (issue #399). Its window and largest output are the ones the
// subscription serves, from CLIProxyAPI's registry, smaller than the API's
// that models.dev gives, which stays beside them; one set on the model wins.
func TestAnAntigravityModelHasTheSubscriptionsWindow(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// The account is served claude-sonnet-4-6, as the owner's is on zm.
	g := modelgateway.New(db, fixedClock{}, platform.NewID,
		func(context.Context, string) (modelgateway.ProviderInfo, error) {
			return modelgateway.ProviderInfo{
				Name: "antigravity-subscription", Kind: "antigravity_subscription", Models: []string{"claude-sonnet-4-6"},
			}, nil
		}, func(context.Context, string) (string, error) { return "", nil })

	m, err := g.ApplyModel(t.Context(),
		"name: claude-sonnet\ntargets: [{provider: antigravity-subscription, model: claude-sonnet-4-6}]\n")
	require.NoError(t, err)

	got, err := g.Resolve(t.Context(), m)
	require.NoError(t, err)

	seed, ok := prices.Find("antigravity_subscription", "claude-sonnet-4-6")
	require.True(t, ok)
	require.NotNil(t, seed.Context)
	assert.Greater(t, *seed.Context, int64(200000), "models.dev gives the API's window")

	assert.Equal(t, modelgateway.Value[int64]{Value: ptr[int64](200000), Source: modelgateway.SourceAntigravity,
		ModelsDev: seed.Context}, got.ContextWindow)
	assert.Equal(t, modelgateway.Value[int64]{Value: ptr[int64](64000), Source: modelgateway.SourceAntigravity,
		ModelsDev: seed.MaxOutput}, got.MaxOutput)

	m, err = g.SetAttributes(t.Context(), "claude-sonnet", modelgateway.Attributes{ContextWindow: ptr[int64](100000)})
	require.NoError(t, err)

	got, err = g.Resolve(t.Context(), m)
	require.NoError(t, err)
	assert.Equal(t, modelgateway.SourceSet, got.ContextWindow.Source)
	assert.Equal(t, modelgateway.SourceAntigravity, got.MaxOutput.Source)
}
