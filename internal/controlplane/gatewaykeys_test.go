package controlplane

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: the owner makes a gateway key on the Gateway keys page for
// opencode on a laptop (issue #199, step 6). Its value is answered once and
// works at the gateway; the page then lists it by its name and start, never
// its value. A worker's key is counted, not listed, and is not the owner's to
// revoke. Revoking the owner's key stops it. A coordinator manages no key.
func TestTheOwnerMakesAndRevokesAGatewayKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, gatewayProviders(svc),
		func(context.Context, string) (string, error) { return "", nil })

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).WithGatewayKeys(gateway).Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := func(kind bearertoken.Kind, name string) *openapi.ClientWithResponses {
		_, secret, err := svc.AddToken(ctx, name, kind)
		require.NoError(t, err)

		c, err := openapi.NewClientWithResponses(srv.URL+clientapi.BasePath,
			openapi.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
				r.Header.Set("Authorization", "Bearer "+secret)

				return nil
			}))
		require.NoError(t, err)

		return c
	}

	owner := client(bearertoken.Owner, "web")
	pi := client(bearertoken.Coordinator, "pi")

	workerKey, err := gateway.AddDispatchKey(ctx, "dsp_1", "gpt-sol")
	require.NoError(t, err)

	refused, err := pi.CreateGatewayKeyWithResponse(ctx, openapi.NewGatewayKey{Name: "opencode-laptop"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	bad, err := owner.CreateGatewayKeyWithResponse(ctx, openapi.NewGatewayKey{Name: "Not A Name"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())

	made, err := owner.CreateGatewayKeyWithResponse(ctx, openapi.NewGatewayKey{Name: "opencode-laptop"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))

	secret := made.JSON201.Secret
	assert.True(t, strings.HasPrefix(secret, made.JSON201.Key.Prefix), "the start shown is the key's own")
	assert.Len(t, made.JSON201.Key.Prefix, len("hyp_gateway_v1_")+6)
	assert.Equal(t, []string{modelgateway.AllModels}, made.JSON201.Key.Models, "no model named: every model")

	works, err := gateway.Authenticate(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, "opencode-laptop", works.Name)

	listed, err := owner.ListGatewayKeysWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 1, "a worker's key is not listed")
	assert.Equal(t, "opencode-laptop", listed.JSON200.Data[0].Name)
	assert.Nil(t, listed.JSON200.Data[0].LastUsedAt)
	assert.Equal(t, 1, listed.JSON200.LiveWorkerKeys)
	assert.NotContains(t, string(listed.Body), secret)

	worker, err := gateway.Authenticate(ctx, workerKey)
	require.NoError(t, err)

	notOwners, err := owner.RevokeGatewayKeyWithResponse(ctx, worker.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, notOwners.StatusCode(), "a worker's key ends with its Dispatch")

	revoked, err := owner.RevokeGatewayKeyWithResponse(ctx, made.JSON201.Key.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, revoked.StatusCode())

	_, err = gateway.Authenticate(ctx, secret)
	require.Error(t, err, "a revoked key no longer works")

	again, err := owner.RevokeGatewayKeyWithResponse(ctx, made.JSON201.Key.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, again.StatusCode())

	listed, err = owner.ListGatewayKeysWithResponse(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed.JSON200.Data)
}
