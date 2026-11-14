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

	held := newFakePhone()
	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: nodeproto.DeviceAndroid,
		KVM: func() bool { return true }, Phone: held,
	}, state, newFakeRunner(), logger)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Logf("the node ended with: %v", err)
		}

		_ = state.Close()
	})

	require.Eventually(t, func() bool {
		_, err := nodes.Files(t.Context(), n.ID, "/srv/wt", workspacefs.Request{Op: workspacefs.OpList})

		return !errors.Is(err, orchestration.ErrUnavailable)
	}, wait, 20*time.Millisecond)

	key, err := svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	body, err := json.Marshal(agenttool.BrowserCall{Tool: devicetool.Tap, Arguments: json.RawMessage(`{"target":"#4"}`)})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+agenttool.PathDeviceCall,
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	answer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(answer))

	assert.JSONEq(t, `{"content":[{"type":"image","data":"cG5n","mimeType":"image/png"},`+
		`{"type":"text","text":"Numbered boxes."}],"isError":false}`, string(answer))

	held.mu.Lock()
	defer held.mu.Unlock()

	assert.Equal(t, []string{`d-dsp_1 device_tap {"target":"#4"}`}, held.calls)
}
