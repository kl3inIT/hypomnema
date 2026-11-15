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

	listed, err := g.ListModels(t.Context())
	require.NoError(t, err)

	names := make([]string, 0, len(listed))
	for _, m := range listed {
		names = append(names, m.Name)
	}

	assert.Contains(t, names, "sol")
	assert.Contains(t, names, "claude-subscription/claude-opus-5-5")
	assert.Contains(t, names, "antigravity-subscription/gemini-3.1-pro-high")
	assert.IsIncreasing(t, names)
}
