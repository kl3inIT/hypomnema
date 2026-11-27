package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Scenario: a worker uses the phone (issue #417, step 2). Its call goes to
// the agent API with its Dispatch's key, the control plane passes it on the
// socket of the node that holds the phone, and the node runs it on its phone
// and posts the result back, a screen as image content, which reaches the
// worker as the phone gave it. The phone knows the worker by its Dispatch.
func TestAWorkersPhoneCallGoesToTheNodeThatHoldsThePhone(t *testing.T) {
	t.Parallel()

	held := newFakePhone()
	r := newPhoneRig(t, held)

	answer, code := r.call(t, devicetool.Tap, `{"target":"#4"}`)
	require.Equal(t, http.StatusOK, code, answer)

	assert.JSONEq(t, `{"content":[{"type":"image","data":"cG5n","mimeType":"image/png"},`+
		`{"type":"text","text":"Numbered boxes."}],"isError":false}`, answer)

	held.mu.Lock()
	defer held.mu.Unlock()

	assert.Equal(t, []string{`d-` + r.dispatch + ` device_tap {"target":"#4"}`}, held.calls)
}

// Scenario: the node that holds the phone goes away while it runs a call,
// as when it restarts to update (trial on AWS, 2026-11-15). The call fails at
// once, saying to call again, rather than waiting out its time limit for an
// answer the new process will never send.
func TestAPhoneCallFailsAtOnceWhenItsNodeGoesAway(t *testing.T) {
	t.Parallel()

	held := newFakePhone()
	held.reached, held.hold = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(held.hold) })

	r := newPhoneRig(t, held)

	type result struct {
		answer string
		code   int
	}

	got := make(chan result, 1)

	go func() {
		answer, code := r.call(t, devicetool.Screen, `{}`)
		got <- result{answer, code}
	}()

	<-held.reached
	r.stopNode()

	select {
	case res := <-got:
		assert.Equal(t, http.StatusServiceUnavailable, res.code, res.answer)
		assert.Contains(t, res.answer, "went away during the call")
	case <-time.After(wait):
		t.Fatal("the call still waits after its node went away")
	}
}

// phoneRig is a control plane with a node that holds a fake phone, and a
// worker's agent key.
type phoneRig struct {
	srv      *httptest.Server
	key      string
	dispatch string
	stopNode func()
}

func newPhoneRig(t *testing.T, held *fakePhone) *phoneRig {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	mux := http.NewServeMux()
	nodes := nodeapi.New(svc, logger)
	nodes.Register(mux)
	agentapi.New(svc, nil, logger).WithDevice(phone.New(phone.Config{Nodes: svc, Link: nodes, Logger: logger})).
		Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	n, token, err := svc.AddNode(t.Context(), "phone", 1)
	require.NoError(t, err)

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)

	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: nodeproto.DeviceAndroid,
		KVM: func() bool { return true }, Phone: held,
	}, state, newFakeRunner(), logger)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	var once sync.Once

	stop := func() {
		once.Do(func() {
			cancel()

			if err := <-done; err != nil {
				t.Logf("the node ended with: %v", err)
			}
		})
	}

	t.Cleanup(func() {
		stop()

		_ = state.Close()
	})

	require.Eventually(t, func() bool {
		_, err := nodes.Files(t.Context(), n.ID, "/srv/wt", workspacefs.Request{Op: workspacefs.OpList})

		return !errors.Is(err, orchestration.ErrUnavailable)
	}, wait, 20*time.Millisecond)

	key, dispatch := phoneDispatch(t, svc)

	return &phoneRig{srv: srv, key: key, dispatch: dispatch, stopNode: stop}
}

func phoneDispatch(t *testing.T, svc *orchestration.Service) (key, dispatch string) {
	t.Helper()
	ctx := t.Context()
	_, err := svc.Profiles().ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [claude-opus-5-5]\n")
	require.NoError(t, err)
	_, _, err = svc.Profiles().ApplyProfile(ctx, "", "name: implementer\ndescription: d\nruntime: claude\n"+
		"model: {provider: gateway}\ntools: [phone]\n")
	require.NoError(t, err)
	node, _, err := svc.AddNode(ctx, "worker", 1)
	require.NoError(t, err)
	require.NoError(t, svc.NodeHello(ctx, node.ID, orchestration.Hello{WorktreeRoot: "/srv/wt",
		Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "2.1.319"}}}))
	require.NoError(t, svc.NodeHeartbeat(ctx, node.ID))
	run, err := svc.CreateRun(ctx, orchestration.NewRun{Objective: "test the phone", CreatedBy: "test"})
	require.NoError(t, err)
	_, err = svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "phone", Spec: "test the phone"}})
	require.NoError(t, err)
	scheduled, err := svc.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, scheduled.Created, 1)
	dispatch = scheduled.Created[0]
	key, err = svc.AddAgentSession(ctx, dispatch, nil)
	require.NoError(t, err)

	return key, dispatch
}

// call is a worker's phone call through the agent API: its answer and status.
func (r *phoneRig) call(t *testing.T, tool, arguments string) (string, int) {
	body, err := json.Marshal(agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})
	if err != nil {
		return err.Error(), 0
	}

	req, err := http.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodPost,
		r.srv.URL+agenttool.PathDeviceCall, bytes.NewReader(body))
	if err != nil {
		return err.Error(), 0
	}

	req.Header.Set("Authorization", "Bearer "+r.key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error(), 0
	}

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return err.Error(), 0
	}

	return string(answer), resp.StatusCode
}
