package controlplane

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// Scenario: the owner opens the Usage page before the gateway served
// anything (issue #199, step 4). A week reads as seven empty days, the first
// at the owner's midnight, and no request; a coordinator reads no usage.
func TestTheOwnerReadsTheGatewaysUsage(t *testing.T) {
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
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).WithGatewayUsage(gateway).Register(mux))

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

	hanoi := 7 * 60
	week, err := owner.GetGatewayUsageWithResponse(ctx, &openapi.GetGatewayUsageParams{
		Period: openapi.GetGatewayUsageParamsPeriodN7d, UtcOffsetMinutes: &hanoi,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, week.StatusCode(), string(week.Body))
	assert.Equal(t, 86400, week.JSON200.BucketSeconds)
	require.Len(t, week.JSON200.Buckets, 7)
	assert.Equal(t, 0, week.JSON200.Buckets[0].Start.In(time.FixedZone("ICT", hanoi*60)).Hour(),
		"a day starts at the owner's midnight")
	assert.Zero(t, week.JSON200.Totals.Requests)
	assert.Empty(t, week.JSON200.ByModel)

	requests, err := owner.ListGatewayRequestsWithResponse(ctx, &openapi.ListGatewayRequestsParams{
		Period: openapi.ListGatewayRequestsParamsPeriodN24h,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, requests.StatusCode(), string(requests.Body))
	assert.Empty(t, requests.JSON200.Data)
	assert.False(t, requests.JSON200.HasMore)

	refused, err := pi.GetGatewayUsageWithResponse(ctx, &openapi.GetGatewayUsageParams{
		Period: openapi.GetGatewayUsageParamsPeriodN24h,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())
}
