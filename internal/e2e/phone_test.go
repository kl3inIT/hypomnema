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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientcli"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// fakePhone is a node's phone that boots at once when started.
type fakePhone struct {
	mu      sync.Mutex
	state   nodeproto.DeviceState
	asked   []string
	changed chan struct{}
}

func newFakePhone() *fakePhone {
	return &fakePhone{state: nodeproto.DeviceState{State: nodeproto.DeviceAbsent}, changed: make(chan struct{}, 1)}
}

func (p *fakePhone) Status() nodeproto.DeviceState {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.state
}

func (p *fakePhone) Start(context.Context) error {
	p.set("start", nodeproto.DeviceState{State: nodeproto.DeviceRunning, Installed: []nodeproto.DeviceComponent{
		{Name: "emulator", Version: "37.3.9"}, {Name: "system-image", Version: "android-34-google_apis_playstore-x86_64-r14"},
	}})

	return nil
}

func (p *fakePhone) Stop(context.Context) {
	p.set("stop", nodeproto.DeviceState{State: nodeproto.DeviceStopped, Installed: p.Status().Installed})
}

func (p *fakePhone) Changed() <-chan struct{} { return p.changed }

func (p *fakePhone) set(asked string, st nodeproto.DeviceState) {
	p.mu.Lock()
	p.asked, p.state = append(p.asked, asked), st
	p.mu.Unlock()

	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// Scenario: the owner starts and stops the phone of a node that holds one,
// through the client API and with the CLI (issue #417). The control plane
// asks on the node's socket, the node says the phone's new state at once,
// and `GET /device` and `device status` show it with what is installed. A
// node that holds no phone is refused, and so is a token that is not the
// owner's.
func TestTheOwnerStartsAndStopsANodesPhone(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	nodes := nodeapi.New(svc, logger)
	nodes.Register(mux)
	require.NoError(t, clientapi.New(svc, logger, stop).WithDevices(nodes).Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	run := func(name, device string, phone daemon.Phone) orchestration.Node {
		n, token, err := svc.AddNode(t.Context(), name, 1)
		require.NoError(t, err)

		state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
		require.NoError(t, err)

		d := daemon.New(daemon.Config{
			URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: device,
			KVM: func() bool { return device != "" }, Phone: phone,
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

		// Ready once its socket is in: a question asked on it is answered.
		require.Eventually(t, func() bool {
			_, err := nodes.Files(t.Context(), n.ID, "/srv/wt", workspacefs.Request{Op: workspacefs.OpList})

			return !errors.Is(err, orchestration.ErrUnavailable)
		}, wait, 20*time.Millisecond)

		return n
	}

	phone := newFakePhone()
	held := run("phone", nodeproto.DeviceAndroid, phone)
	plain := run("build", "", nil)

	_, owner, err := svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)

	_, coordinator, err := svc.AddToken(t.Context(), "coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	call := func(token, method, path string) (int, []byte) {
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+clientapi.BasePath+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		defer resp.Body.Close() //nolint:errcheck // a test's response body

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp.StatusCode, body
	}

	stateOf := func() string {
		status, body := call(owner, http.MethodGet, "/device")
		require.Equal(t, http.StatusOK, status, string(body))

		var got struct {
			Data []struct {
				NodeID string  `json:"node_id"`
				State  *string `json:"state"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(body, &got))
		require.Len(t, got.Data, 1, "only the node that holds a phone is listed")
		require.Equal(t, held.ID, got.Data[0].NodeID)

		if got.Data[0].State == nil {
			return ""
		}

		return *got.Data[0].State
	}

	assert.Equal(t, nodeproto.DeviceAbsent, stateOf(), "the state said at hello")

	status, _ := call(coordinator, http.MethodGet, "/device")
	assert.Equal(t, http.StatusForbidden, status)

	status, _ = call(coordinator, http.MethodPost, "/nodes/"+held.ID+"/device/start")
	assert.Equal(t, http.StatusForbidden, status)

	status, body := call(owner, http.MethodPost, "/nodes/"+plain.ID+"/device/start")
	assert.Equal(t, http.StatusConflict, status, string(body))

	status, body = call(owner, http.MethodPost, "/nodes/"+held.ID+"/device/start")
	require.Equal(t, http.StatusAccepted, status, string(body))
	require.Eventually(t, func() bool { return stateOf() == nodeproto.DeviceRunning }, wait, 20*time.Millisecond,
		"the node says the new state at once, not at its next heartbeat")

	// The CLI, logged in with the owner's token.
	home := t.TempDir()
	tokenFile := filepath.Join(home, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(owner), 0o600))

	cli := func(args ...string) string {
		var out bytes.Buffer
		require.NoError(t, clientcli.Command(t.Context(), home, args, strings.NewReader(""), &out))

		return out.String()
	}

	cli("login", "--url", srv.URL, "--token-file", tokenFile)

	assert.Contains(t, cli("device", "status"),
		held.ID+"\tphone\tkvm\trunning\temulator 37.3.9, system-image android-34-google_apis_playstore-x86_64-r14")

	cli("device", "stop", "phone")
	require.Eventually(t, func() bool { return stateOf() == nodeproto.DeviceStopped }, wait, 20*time.Millisecond)

	phone.mu.Lock()
	asked := slices.Clone(phone.asked)
	phone.mu.Unlock()

	assert.Equal(t, []string{"start", "stop"}, asked)
}
