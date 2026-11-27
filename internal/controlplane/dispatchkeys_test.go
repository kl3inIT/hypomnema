package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: a Task whose model the gateway serves is offered to a node that
// has the CLI and no account. The offer carries a gateway key that works for
// that model; a Task under an account on the node gets none. When the Run is
// canceled the key stops working.
func TestAnOfferCarriesTheGatewaysKeyUntilTheDispatchSettles(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, gatewayProviders(svc),
		func(context.Context, string) (string, error) { return "", nil })
	keys := dispatchKeys{gateway: gateway, svc: svc, logger: logger}

	mux := http.NewServeMux()
	nodeapi.New(svc, logger).WithModelKeys(keys.Key).WithModelSizes(keys.Sizes).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err = svc.Profiles().ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [gpt-sol]\n")
	require.NoError(t, err)
	_, err = gateway.ApplyModel(ctx, "name: gpt-sol\ntargets: [{provider: codex-subscription, model: gpt-6.1-sol}]\n")
	require.NoError(t, err)
	window, output := int64(400_000), int64(64_000)
	_, err = gateway.SetAttributes(ctx, "gpt-sol", modelgateway.Attributes{ContextWindow: &window, MaxOutput: &output})
	require.NoError(t, err)
	_, _, err = svc.Profiles().ApplyProfile(ctx, "",
		"name: codex-worker\ndescription: d\nruntime: codex\nmodel: {provider: gateway, name: gpt-sol}\n")
	require.NoError(t, err)
	_, _, err = svc.Profiles().ApplyProfile(ctx, "", "name: codex-own\ndescription: d\nruntime: codex\n")
	require.NoError(t, err)
	_, err = svc.AddAccount(ctx, "codex", "acct_1")
	require.NoError(t, err)

	_, token, err := svc.AddNode(ctx, "node_a", 4)
	require.NoError(t, err)

	hello, err := json.Marshal(nodeproto.HelloRequest{
		InstanceID: "inst_1", DaemonVersion: "test", WorktreeRoot: "/srv/wt",
		Runtimes: []nodeproto.RuntimeInfo{
			{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "2.1.319"},
			{Runtime: "codex", Path: "/usr/local/bin/codex", Version: "0.159.0"},
		},
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+nodeproto.PathHello, bytes.NewReader(hello))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	//nolint:bodyclose // the library closes the body of a successful upgrade
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+nodeproto.PathSocket,
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	run, err := svc.CreateRun(ctx, orchestration.NewRun{Objective: "o", CreatedBy: "owner"})
	require.NoError(t, err)
	_, err = svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{
		{Ref: "g", Profile: "codex-worker", Spec: "through the gateway"},
		{Ref: "c", Profile: "codex-own", Spec: "under its own account"},
	})
	require.NoError(t, err)

	sch, err := svc.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, sch.Created, 2)

	offers := map[string]nodeproto.Offer{}

	for len(offers) < 2 {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		var msg nodeproto.Message

		err := wsjson.Read(readCtx, conn, &msg)

		cancel()
		require.NoError(t, err)

		if msg.Type != nodeproto.TypeOffer {
			continue
		}

		var o nodeproto.Offer
		require.NoError(t, json.Unmarshal(msg.Payload, &o))
		offers[o.Spec] = o
	}

	own := offers["under its own account"]
	assert.Empty(t, own.ModelKey)
	assert.Zero(t, own.ModelContextWindow, "sizes go with the gateway's models only")

	through := offers["through the gateway"]
	assert.Equal(t, profilestore.GatewayAccountRef, through.AccountRef)
	assert.Equal(t, "codex", through.Runtime)
	require.NotEmpty(t, through.ModelKey)
	assert.Equal(t, window, through.ModelContextWindow, "the gateway's window goes with the offer (issue #1019)")
	assert.Equal(t, output, through.ModelMaxOutput)

	key, err := gateway.Authenticate(ctx, through.ModelKey)
	require.NoError(t, err)
	assert.Equal(t, through.DispatchID, key.DispatchID)
	assert.Equal(t, []string{"gpt-sol"}, key.Models)

	keys.sweep(ctx)

	_, err = gateway.Authenticate(ctx, through.ModelKey)
	require.NoError(t, err, "the key works while the Dispatch is not settled")

	require.NoError(t, svc.CancelRun(ctx, run.ID))
	keys.sweep(ctx)

	_, err = gateway.Authenticate(ctx, through.ModelKey)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)
}
