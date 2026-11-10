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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/terminal"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// echoShell is a shell that greets, repeats what is typed, and ends when
// "exit" is typed.
type echoShell struct {
	out    chan []byte
	closed chan struct{}
	once   sync.Once
}

func newEchoShell() *echoShell {
	s := &echoShell{out: make(chan []byte, 64), closed: make(chan struct{})}
	s.out <- []byte("welcome\r\n")

	return s
}

func (s *echoShell) Read(p []byte) (int, error) {
	select {
	case data := <-s.out:
		return copy(p, data), nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *echoShell) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "exit") {
		_ = s.Close()

		return len(p), nil
	}

	s.out <- []byte("you typed " + string(p))

	return len(p), nil
}

func (s *echoShell) Resize(uint16, uint16) error { return nil }

func (s *echoShell) Close() error {
	s.once.Do(func() { close(s.closed) })

	return nil
}

// terminalWorld is a control plane with its client API and node API, and a
// node whose shells are echo shells.
type terminalWorld struct {
	t      *testing.T
	svc    *orchestration.Service
	url    string
	node   orchestration.Node
	shells *terminal.Manager
	mu     sync.Mutex
	starts int
}

func newTerminalWorld(t *testing.T, withTerminal bool) *terminalWorld {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	logger := slog.New(slog.DiscardHandler)
	w := &terminalWorld{t: t, svc: orchestration.New(db, platform.SystemClock{}, platform.NewID)}

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	nodes := nodeapi.New(w.svc, logger)
	nodes.Register(mux)

	api := clientapi.New(w.svc, logger, stop).WithTerminals(
		func(ctx context.Context, nodeID, sessionID string, rows, cols int) (clientapi.TerminalEnd, error) {
			end, err := nodes.OpenTerminal(ctx, nodeID, sessionID, rows, cols)
			if err != nil {
				return nil, err
			}

			return end, nil
		})
	require.NoError(t, api.Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	w.url = srv.URL

	node, token, err := w.svc.AddNode(t.Context(), "node_a", 2)
	require.NoError(t, err)

	w.node = node

	if withTerminal {
		w.shells = terminal.New(terminal.Options{Start: func(uint16, uint16) (terminal.Shell, error) {
			w.mu.Lock()
			defer w.mu.Unlock()

			w.starts++

			return newEchoShell(), nil
		}})
		t.Cleanup(w.shells.Close)
	}

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "node", "state.db"), time.Now)
	require.NoError(t, err)

	d := daemon.New(daemon.Config{
		URL: w.url, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Terminal: w.shells,
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
		n, err := w.svc.GetNode(t.Context(), node.ID)

		return err == nil && n.Connected
	}, wait, 20*time.Millisecond)

	return w
}

func (w *terminalWorld) token(kind bearertoken.Kind) string {
	w.t.Helper()

	_, secret, err := w.svc.AddToken(w.t.Context(), "t-"+string(kind), kind)
	require.NoError(w.t, err)

	return secret
}

// ticket asks for a terminal ticket and returns the status with the ticket.
func (w *terminalWorld) ticket(token, session string) (int, string) {
	w.t.Helper()

	body, err := json.Marshal(map[string]string{"session_id": session})
	require.NoError(w.t, err)

	req, err := http.NewRequestWithContext(w.t.Context(), http.MethodPost,
		w.url+clientapi.BasePath+"/nodes/"+w.node.ID+"/terminal", bytes.NewReader(body))
	require.NoError(w.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(w.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	var got struct {
		Ticket string `json:"ticket"`
	}

	_ = json.NewDecoder(resp.Body).Decode(&got)

	return resp.StatusCode, got.Ticket
}

// page is the web client's end of a terminal.
type page struct {
	t      *testing.T
	conn   *websocket.Conn
	mu     sync.Mutex
	screen strings.Builder
	closed chan string
}

// open opens the terminal's WebSocket with a ticket; nil with the HTTP
// status when the server refuses it.
func (w *terminalWorld) open(ticket string) (*page, int) {
	w.t.Helper()

	target := "ws" + strings.TrimPrefix(w.url, "http") + clientapi.BasePath + "/terminals/" + ticket +
		"?height=24&width=80"

	conn, resp, err := websocket.Dial(w.t.Context(), target, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		require.NotNil(w.t, resp, err)

		return nil, resp.StatusCode
	}

	p := &page{t: w.t, conn: conn, closed: make(chan string, 1)}

	go func() {
		for {
			_, data, err := conn.Read(context.WithoutCancel(w.t.Context()))
			if err != nil {
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					p.closed <- ce.Reason
				} else {
					p.closed <- ""
				}

				return
			}

			p.mu.Lock()
			p.screen.Write(data)
			p.mu.Unlock()
		}
	}()

	w.t.Cleanup(func() { _ = conn.CloseNow() })

	return p, http.StatusSwitchingProtocols
}

func (p *page) typeText(text string) {
	p.t.Helper()

	msg, err := json.Marshal(map[string]string{"data": text})
	require.NoError(p.t, err)
	require.NoError(p.t, p.conn.Write(p.t.Context(), websocket.MessageText, msg))
}

func (p *page) shows(want string) {
	p.t.Helper()

	require.Eventually(p.t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()

		return strings.Contains(p.screen.String(), want)
	}, wait, 20*time.Millisecond, "the page never showed %q", want)
}

// Scenario: the owner opens a terminal on a node, types, reloads the page
// and comes back. What is typed reaches the node's shell and its output the
// page; after the reload the same shell is there, and the page is first
// shown what it had printed. When the shell exits, the page is told so.
func TestOwnerUsesATerminalAcrossAReload(t *testing.T) {
	t.Parallel()

	w := newTerminalWorld(t, true)
	owner := w.token(bearertoken.Owner)

	status, ticket := w.ticket(owner, "11111111-2222-3333-4444-555555555555")
	require.Equal(t, http.StatusCreated, status)

	first, _ := w.open(ticket)
	require.NotNil(t, first)
	first.shows("welcome")
	first.typeText("ls\n")
	first.shows("you typed ls")

	require.NoError(t, first.conn.Close(websocket.StatusGoingAway, "reload"))

	status, ticket = w.ticket(owner, "11111111-2222-3333-4444-555555555555")
	require.Equal(t, http.StatusCreated, status)

	back, _ := w.open(ticket)
	require.NotNil(t, back)
	back.shows("you typed ls")
	back.typeText("pwd\n")
	back.shows("you typed pwd")

	w.mu.Lock()
	assert.Equal(t, 1, w.starts, "the reload came back to the same shell")
	w.mu.Unlock()

	back.typeText("exit\n")

	select {
	case reason := <-back.closed:
		assert.Equal(t, terminal.ReasonShellExited, reason)
	case <-time.After(wait):
		t.Fatal("the page was not told the shell ended")
	}
}

// Scenario: who may open a terminal and with what. A coordinator token gets
// no ticket; a ticket opens one WebSocket and no second; a made-up ticket
// opens none.
func TestTerminalTicketIsTheOwnersAndWorksOnce(t *testing.T) {
	t.Parallel()

	w := newTerminalWorld(t, true)

	status, _ := w.ticket(w.token(bearertoken.Coordinator), "11111111-2222-3333-4444-555555555555")
	assert.Equal(t, http.StatusForbidden, status)

	status, ticket := w.ticket(w.token(bearertoken.Owner), "11111111-2222-3333-4444-555555555555")
	require.Equal(t, http.StatusCreated, status)

	p, _ := w.open(ticket)
	require.NotNil(t, p)
	p.shows("welcome")

	again, status := w.open(ticket)
	assert.Nil(t, again)
	assert.Equal(t, http.StatusNotFound, status)

	forged, status := w.open("0000000000000000000000000000000000000000")
	assert.Nil(t, forged)
	assert.Equal(t, http.StatusNotFound, status)
}

// Scenario: a node that opens no terminal, because its own configuration
// turned it off or its version has none. The owner gets no ticket, with the
// reason, instead of a terminal that never connects.
func TestNodeWithoutATerminalGivesNoTicket(t *testing.T) {
	t.Parallel()

	w := newTerminalWorld(t, false)

	status, _ := w.ticket(w.token(bearertoken.Owner), "11111111-2222-3333-4444-555555555555")
	assert.Equal(t, http.StatusConflict, status)
}
