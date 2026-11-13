package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// gatewayOver is a model gateway over a database of its own, with the
// world's providers.
func gatewayOver(t *testing.T, svc *orchestration.Service) *modelgateway.Service {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			p, err := svc.GetProvider(ctx, name)
			if err != nil {
				return modelgateway.ProviderInfo{}, fmt.Errorf("%w: %w", modelgateway.ErrNotFound, err)
			}

			return modelgateway.ProviderInfo{Name: p.Name, Kind: p.Kind, Models: p.Models}, nil
		}, func(context.Context, string) (string, error) { return "", nil })
}

// Scenario: the owner opens a gateway model on the Models page (issue #367).
// Each attribute comes with where it came from: the Codex model's window from
// models.dev, with its price as its usage is counted. The owner sets a window
// of the model's own, which wins with models.dev's beside it, and clears it
// again. A name with a slash is sent as %2F. A coordinator reads and sets
// nothing; an effort level Claude is not given is refused.
func TestTheOwnerSetsAGatewayModelsAttributes(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	g := gatewayOver(t, w.svc)
	w.srv.WithGatewayModels(g)

	_, err := g.ApplyModel(ctx, "name: cx/sol\ntargets: [{provider: codex-subscription, model: gpt-6.1-sol}]\n")
	require.NoError(t, err)
	_, err = g.ApplyModel(ctx, "name: sonnet\ntargets: [{provider: claude-subscription, model: claude-sonnet-5-5}]\n")
	require.NoError(t, err)

	owner, _ := w.token("web", bearertoken.Owner)
	coord, _ := w.token("pi", bearertoken.Coordinator)

	seed, ok := prices.Find("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)

	listed, err := coord.ListGatewayModelsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 2)

	sol := listed.JSON200.Data[0]
	assert.Equal(t, "cx/sol", sol.Name)
	codexWindow := int64(272_000)
	assert.Equal(t, openapi.GatewayModelTokens{
		Value: &codexWindow, Source: openapi.GatewayModelSourceCodex, ModelsDev: seed.Context,
	}, sol.Attributes.ContextWindow, "a Codex subscription's window is Codex's own")
	assert.Equal(t, &[]openapi.GatewayModelInputKind{"text", "image"}, sol.Attributes.Input.Value)
	require.NotNil(t, sol.Attributes.Price)
	assert.InDelta(t, float64(*seed.Input)/1e6, sol.Attributes.Price.Input, 1e-9, "dollars per million tokens")

	window := int64(200_000)

	refused, err := coord.SetGatewayModelAttributesWithResponse(ctx, "cx/sol",
		openapi.GatewayModelSettings{ContextWindow: &window})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	set, err := owner.SetGatewayModelAttributesWithResponse(ctx, "cx/sol",
		openapi.GatewayModelSettings{ContextWindow: &window})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode(), string(set.Body))
	assert.Equal(t, openapi.GatewayModelTokens{
		Value: &window, Source: openapi.GatewayModelSourceSet, ModelsDev: seed.Context,
	}, set.JSON200.Attributes.ContextWindow)
	assert.Equal(t, openapi.GatewayModelSourceModelsDev, set.JSON200.Attributes.MaxOutput.Source)

	cleared, err := owner.SetGatewayModelAttributesWithResponse(ctx, "cx/sol", openapi.GatewayModelSettings{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cleared.StatusCode(), string(cleared.Body))
	assert.Equal(t, openapi.GatewayModelSourceCodex, cleared.JSON200.Attributes.ContextWindow.Source)

	minimal := []openapi.GatewayModelEffort{openapi.GatewayModelEffortMinimal}
	bad, err := owner.SetGatewayModelAttributesWithResponse(ctx, "sonnet",
		openapi.GatewayModelSettings{Efforts: &minimal})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode(), string(bad.Body))

	missing, err := owner.SetGatewayModelAttributesWithResponse(ctx, "nope", openapi.GatewayModelSettings{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())
}
