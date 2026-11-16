package modelgateway_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// newSubscriptionGateway returns a gateway over a migrated database, with its
// built-in subscriptions, that is given its list of providers.
func newSubscriptionGateway(t *testing.T) *modelgateway.Service {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	info := func(p orchestration.Provider) modelgateway.ProviderInfo {
		return modelgateway.ProviderInfo{Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Models: p.Models}
	}

	return modelgateway.New(db, fixedClock{}, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			p, err := orch.GetProvider(ctx, name)
			if err != nil {
				return modelgateway.ProviderInfo{}, fmt.Errorf("%w: %w", modelgateway.ErrNotFound, err)
			}

			return info(p), nil
		}, func(context.Context, string) (string, error) { return "", nil }).
		WithProviderList(func(ctx context.Context) ([]modelgateway.ProviderInfo, error) {
			providers, err := orch.ListProviders(ctx)
			if err != nil {
				return nil, err
			}

			out := make([]modelgateway.ProviderInfo, 0, len(providers))
			for _, p := range providers {
				out = append(out, info(p))
			}

			return out, nil
		})
}

// Scenario: a subscription's model is served as "<subscription>/<model>"
// with no row in the gateway's table; a model the subscription does not
// offer, or a provider that is not a subscription, is not found.
func TestASubscriptionsModelIsServedUnderItsName(t *testing.T) {
	t.Parallel()

	g := newSubscriptionGateway(t)

	m, err := g.GetModel(t.Context(), "claude-subscription/claude-opus-5-5")
	require.NoError(t, err)
	assert.Equal(t, "claude-subscription/claude-opus-5-5", m.Name)
	assert.Equal(t, []modelgateway.Target{{Provider: "claude-subscription", UpstreamModel: "claude-opus-5-5"}},
		m.Targets)

	for _, name := range []string{"claude-subscription/gpt-unknown", "nobody/claude-opus-5-5", "claude-opus-5-5"} {
		_, err = g.GetModel(t.Context(), name)
		require.ErrorIs(t, err, modelgateway.ErrNotFound, name)
	}
}

// Scenario: the gateway's list holds every subscription's models next to the
// names made in its table.
func TestTheListHoldsEverySubscriptionsModels(t *testing.T) {
	t.Parallel()

	g := newSubscriptionGateway(t)

	_, err := g.ApplyModel(t.Context(),
		"name: sol\ntargets: [{provider: claude-subscription, model: claude-sonnet-5-5}]\n")
	require.NoError(t, err)

	// A name in the table that is also a subscription's is listed once, from its row.
	_, err = g.ApplyModel(t.Context(), "name: claude-subscription/claude-haiku-5-5\n"+
		"targets: [{provider: claude-subscription, model: claude-haiku-5-5}]\n")
	require.NoError(t, err)

	listed, err := g.ListModels(t.Context())
	require.NoError(t, err)

	names := make([]string, 0, len(listed))
	for _, m := range listed {
		names = append(names, m.Name)
	}

	assert.Contains(t, names, "sol")
	assert.Contains(t, names, "claude-subscription/claude-opus-5-5")
	assert.Contains(t, names, "antigravity-subscription/gemini-3.1-pro-high")
	assert.IsIncreasing(t, names, "each name once")
}

// Scenario: a subscription's model has no row whose attributes could be set; setting them is refused and says why.
func TestASubscriptionsModelHasNoAttributesToSet(t *testing.T) {
	t.Parallel()

	g := newSubscriptionGateway(t)

	_, err := g.SetAttributes(t.Context(), "claude-subscription/claude-opus-5-5", modelgateway.Attributes{})
	require.ErrorIs(t, err, modelgateway.ErrInvalid)
	assert.ErrorContains(t, err, "is a subscription's")
}

// Scenario: what Google serves an Antigravity model with gives its window and largest output (issue #587), before
// the copied table; a model Google says nothing of keeps the table's or models.dev's, a value the model sets wins,
// and another kind of provider is not asked.
func TestAnAntigravityModelHasTheWindowGoogleServesItWith(t *testing.T) {
	t.Parallel()

	var asked []string

	g := newSubscriptionGateway(t).WithServedLimits(func(_ context.Context, kind, upstream string) (int64, int64, bool) {
		asked = append(asked, kind+" "+upstream)

		switch upstream {
		case "gemini-3.8-flash-low":
			return 1048576, 65536, true
		case "claude-sonnet-4-6":
			return 250000, 64000, true
		}

		return 0, 0, false
	})
	ctx := t.Context()

	resolve := func(model modelgateway.Model) modelgateway.Resolved {
		t.Helper()

		got, err := g.Resolve(ctx, model)
		require.NoError(t, err)

		return got
	}
	target := func(provider, upstream string) modelgateway.Model {
		return modelgateway.Model{Name: provider + "/" + upstream, Targets: []modelgateway.Target{
			{Provider: provider, UpstreamModel: upstream},
		}}
	}

	low := resolve(target("antigravity-subscription", "gemini-3.8-flash-low"))
	require.NotNil(t, low.ContextWindow.Value, "known now, where the table has only -high")
	assert.Equal(t, int64(1048576), *low.ContextWindow.Value)
	assert.Equal(t, int64(65536), *low.MaxOutput.Value)
	assert.Equal(t, modelgateway.SourceAntigravity, low.ContextWindow.Source)

	sonnet := resolve(target("antigravity-subscription", "claude-sonnet-4-6"))
	assert.Equal(t, int64(250000), *sonnet.ContextWindow.Value, "Google's, not the table's 200000")

	gpt := resolve(target("antigravity-subscription", "gpt-oss-120b-medium"))
	assert.Equal(t, int64(114000), *gpt.ContextWindow.Value, "Google said nothing: the table's")

	window := int64(500000)
	set := target("antigravity-subscription", "claude-sonnet-4-6")
	set.Attributes.ContextWindow = &window
	assert.Equal(t, window, *resolve(set).ContextWindow.Value, "a value the model sets wins")

	asked = nil
	resolve(target("claude-subscription", "claude-opus-5-5"))
	assert.Empty(t, asked, "only Antigravity's models are asked of")
}
