package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// fakeScreen is the node's view of its phone: what the test has it say, and
// the owner's input it was given.
type fakeScreen struct {
	says   chan []byte
	inputs chan nodeproto.DeviceViewMessage
	closed chan struct{}
}

func (s *fakeScreen) Next(ctx context.Context) ([]byte, *nodeproto.DeviceViewMessage, error) {
	select {
	case raw := <-s.says:
		if bytes.HasPrefix(raw, []byte("SEMU")) {
			return raw, nil, nil
		}

		var m nodeproto.DeviceViewMessage
		_ = json.Unmarshal(raw, &m)

		return nil, &m, nil
	case <-s.closed:
		return nil, nil, context.Canceled
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (s *fakeScreen) Input(in nodeproto.DeviceViewMessage) error {
	s.inputs <- in

	return nil
}

func (s *fakeScreen) Close() { close(s.closed) }

// Scenario: the owner watches the phone from the web and takes it over
// (issue #417, step 3). The page's ticket opens a WebSocket on the control
// plane, which asks the node that holds the phone for a view on the node's
// socket; the node opens a connection of its own and the phone's state and
// frames come through it to the page. The owner's touch before taking control
// does not reach the phone; after, it does, and an agent's phone call is
// answered paused meanwhile. Closing the page closes the node's view and gives
// the phone back.
func TestTheOwnerWatchesAndTakesOverThePhoneThroughTheNode(t *testing.T) {
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

	shared := phone.New(phone.Config{
		Nodes: svc, Link: nodes, Logger: logger,
		Views: func(ctx context.Context, nodeID string) (phone.ViewEnd, error) {
			return nodes.OpenDeviceView(ctx, nodeID)
		},
	})
	require.NoError(t, clientapi.New(svc, logger, stop).WithPhone(ownersPhone{shared}).Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	n, token, err := svc.AddNode(t.Context(), "phone", 1)
	require.NoError(t, err)

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)

	screen := &fakeScreen{
		says: make(chan []byte, 8), inputs: make(chan nodeproto.DeviceViewMessage, 8), closed: make(chan struct{}),
	}
	held := newFakePhone()
	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: nodeproto.DeviceAndroid,
		KVM: func() bool { return true }, Phone: held,
		WatchPhone: func(context.Context) daemon.PhoneView { return screen },
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

	require.Eventually(t, func() bool { return nodes.Online(n.ID) }, wait, 20*time.Millisecond)

	_, owner, err := svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+clientapi.BasePath+"/device/views", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+owner)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	var ticket struct{ Ticket string }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ticket))
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	page, _, err := websocket.Dial(t.Context(), //nolint:bodyclose // A WebSocket: no body is left open.
		"ws"+strings.TrimPrefix(srv.URL, "http")+clientapi.BasePath+"/device/views/"+ticket.Ticket, nil)
	require.NoError(t, err)

	read := func() (websocket.MessageType, string) {
		t.Helper()

		readCtx, cancel := context.WithTimeout(t.Context(), wait)
		defer cancel()

		kind, data, err := page.Read(readCtx)
		require.NoError(t, err)

		return kind, string(data)
	}

	_, said := read()
	assert.JSONEq(t, `{"type":"control","controller":"agent"}`, said)

	screen.says <- []byte(`{"type":"state","state":"running"}`)
	screen.says <- []byte("SEMU a key frame")

	_, said = read()
	assert.JSONEq(t, `{"type":"state","state":"running"}`, said)

	kind, frame := read()
	assert.Equal(t, websocket.MessageBinary, kind)
	assert.Equal(t, "SEMU a key frame", frame)

	send := func(message string) {
		t.Helper()
		require.NoError(t, page.Write(t.Context(), websocket.MessageText, []byte(message)))
	}

	send(`{"type":"touch","action":"down","x":0.5,"y":0.5}`)

	_, said = read()
	assert.JSONEq(t, `{"type":"control","controller":"agent"}`, said, "input without control is dropped")

	send(`{"type":"takeControl"}`)

	_, said = read()
	assert.JSONEq(t, `{"type":"control","controller":"you"}`, said)

	send(`{"type":"touch","action":"down","x":0.5,"y":0.25}`)
	assert.Equal(t, nodeproto.DeviceViewMessage{Type: "touch", Action: "down", X: 0.5, Y: 0.25}, <-screen.inputs)

	answer, err := shared.Call(t.Context(), "d-dsp_1", devicetool.Screen, nil)
	require.NoError(t, err)
	assert.Contains(t, string(answer), "paused while the owner controls the phone")

	require.NoError(t, page.Close(websocket.StatusNormalClosure, ""))

	select {
	case <-screen.closed:
	case <-time.After(wait):
		t.Fatal("the node's view was not closed")
	}

	assert.Eventually(t, func() bool { return !shared.Controlled() }, wait, 10*time.Millisecond)
}

// ownersPhone is the phone as the client API sees it, as the control plane
// wires it.
type ownersPhone struct{ *phone.Phone }

func (p ownersPhone) Watch(ctx context.Context) (clientapi.PhoneView, error) {
	view, err := p.Phone.Watch(ctx)
	if err != nil {
		return nil, err
	}

	return view, nil
}
