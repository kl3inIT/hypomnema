package clientapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A terminal on a node, from the web (issue #116).
//
// The page first asks for a ticket with an authorized request, then opens a
// WebSocket named by the ticket, which works once and for a short time: as
// Nezha's POST /terminal followed by GET /ws/terminal/:id
// (cmd/dashboard/controller/terminal.go). A browser cannot put the token on
// a WebSocket, and the ticket keeps the token out of the address.
//
// On the WebSocket the page sends JSON, {data} for what is typed and
// {height, width} for a resize, and receives the shell's bytes in binary
// frames; the control plane only copies both ways, as Coder's
// coderd/workspaceapps/proxy.go does (read for how it behaves, not copied).

const (
	// terminalTicketFor is how long a ticket waits for its WebSocket.
	terminalTicketFor = 30 * time.Second
	// terminalMessageBytes bounds one message from the page.
	terminalMessageBytes = 64 << 10
	// The terminal's size when the page names none.
	terminalRows, terminalCols = 24, 80
)

// terminalSessionID is what a session's id may be: the UUID the page makes.
var terminalSessionID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// TerminalEnd is the node's side of one terminal connection.
type TerminalEnd interface {
	// Read returns the shell's next output.
	Read(ctx context.Context) ([]byte, error)
	// Type sends what was typed.
	Type(ctx context.Context, data []byte) error
	// Resize sends a nodeproto.TerminalResize as JSON.
	Resize(ctx context.Context, size []byte) error
	Close(reason string)
}

// Terminals asks a node to connect a terminal session.
type Terminals func(ctx context.Context, nodeID, sessionID string, rows, cols int) (TerminalEnd, error)

// WithTerminals sets how a terminal reaches a node.
func (s *Server) WithTerminals(t Terminals) *Server {
	s.terminals = t

	return s
}

// terminalTicket is one permission to open a terminal's WebSocket.
type terminalTicket struct {
	nodeID    string
	sessionID string
	// by and from say who asked, for the record.
	by      string
	from    string
	expires time.Time
}

// terminalTickets are the tickets not used yet.
type terminalTickets struct {
	mu      sync.Mutex
	tickets map[string]terminalTicket
}

func (t *terminalTickets) add(ticket terminalTicket) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a terminal ticket: %w", err)
	}

	id := hex.EncodeToString(raw)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.tickets == nil {
		t.tickets = map[string]terminalTicket{}
	}

	now := time.Now()
	for old, tk := range t.tickets {
		if now.After(tk.expires) {
			delete(t.tickets, old)
		}
	}

	t.tickets[id] = ticket

	return id, nil
}

// take returns a ticket once, if it has not expired.
func (t *terminalTickets) take(id string) (terminalTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ticket, ok := t.tickets[id]
	delete(t.tickets, id)

	return ticket, ok && time.Now().Before(ticket.expires)
}

// OpenNodeTerminal gives the owner a ticket for a terminal on a node. The
// node must be connected and must have said it opens terminals.
func (s *Server) OpenNodeTerminal(ctx context.Context, req openapi.OpenNodeTerminalRequestObject,
) (openapi.OpenNodeTerminalResponseObject, error) {
	if s.terminals == nil {
		return nil, fmt.Errorf("%w: this control plane opens no terminals", orchestration.ErrInvalid)
	}

	if !terminalSessionID.MatchString(req.Body.SessionId) {
		return nil, fmt.Errorf("%w: a terminal session id is 8 to 64 letters, digits and dashes", orchestration.ErrInvalid)
	}

	n, err := s.svc.GetNode(ctx, req.NodeId)
	if err != nil {
		return nil, err
	}

	switch {
	case !n.Connected:
		return nil, fmt.Errorf("%w: node %s is not connected", orchestration.ErrUnavailable, n.Name)
	case !n.Terminal:
		return nil, fmt.Errorf("%w: node %s opens no terminal: its version has none, or its own configuration turned it off",
			orchestration.ErrConflict, n.Name)
	}

	expires := time.Now().Add(terminalTicketFor)

	ticket, err := s.tickets.add(terminalTicket{
		nodeID: n.ID, sessionID: req.Body.SessionId, by: principal(ctx).Name, from: callerOf(ctx).address,
		expires: expires,
	})
	if err != nil {
		return nil, err
	}

	return openapi.OpenNodeTerminal201JSONResponse{Ticket: ticket, ExpiresAt: expires.UTC()}, nil
}

// terminalMessage is what the page sends: what was typed, or a new size.
type terminalMessage struct {
	Data   *string `json:"data"`
	Height int     `json:"height"`
	Width  int     `json:"width"`
}

// terminalSocket is the terminal's WebSocket. The ticket in its path is the
// permission; the origin must be the web client's own, which the WebSocket
// library checks against the request's host (Coder allows only the
// dashboard's hosts).
func (s *Server) terminalSocket(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.tickets.take(r.PathValue("ticket"))
	if !ok || s.terminals == nil {
		http.Error(w, "unknown or expired terminal ticket", http.StatusNotFound)

		return
	}

	rows, cols := queryInt(r, "height", terminalRows), queryInt(r, "width", terminalCols)
	if !nodeproto.ValidTerminalSize(rows, cols) {
		http.Error(w, "a terminal of this size is not allowed", http.StatusBadRequest)

		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(terminalMessageBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// A shutdown ends the terminal with the server's other long requests.
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	end, err := s.terminals(ctx, ticket.nodeID, ticket.sessionID, rows, cols)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, closeReason(err.Error()))

		return
	}

	who := []any{
		slog.String("node_id", ticket.nodeID), slog.String("session_id", ticket.sessionID),
		slog.String("by", ticket.by), slog.String("from", ticket.from),
	}
	started := time.Now()

	s.logger.InfoContext(ctx, "terminal opened",
		append([]any{slog.String("event", "clientapi.terminal_opened")}, who...)...)

	reason := s.copyTerminal(ctx, conn, end)

	s.logger.InfoContext(context.WithoutCancel(ctx), "terminal closed", append([]any{
		slog.String("event", "clientapi.terminal_closed"), slog.Duration("lasted", time.Since(started).Round(time.Second)),
		slog.String("reason", reason),
	}, who...)...)
}

// copyTerminal copies between the page and the node until either leaves and
// returns why the node's end closed, "" when the page left first. The page
// is closed with that reason, so it can tell a shell that ended from a
// connection that dropped.
func (s *Server) copyTerminal(ctx context.Context, conn *websocket.Conn, end TerminalEnd) string {
	nodeLeft := make(chan string, 1)

	go func() {
		reason := ""

		defer func() {
			nodeLeft <- reason

			_ = conn.Close(websocket.StatusNormalClosure, closeReason(reason))
		}()

		for {
			data, err := end.Read(ctx)
			if err != nil {
				reason = nodeCloseReason(err)

				return
			}

			if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
				return
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}

		var msg terminalMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}

		if msg.Data != nil {
			if err := end.Type(ctx, []byte(*msg.Data)); err != nil {
				break
			}
		}

		if nodeproto.ValidTerminalSize(msg.Height, msg.Width) {
			size, err := json.Marshal(nodeproto.TerminalResize{Height: msg.Height, Width: msg.Width})
			if err != nil || end.Resize(ctx, size) != nil {
				break
			}
		}
	}

	// Closing the node's end makes its reader return, whichever end left first.
	end.Close("the page left")

	return <-nodeLeft
}

// nodeCloseReason is why the node closed its end, when it said.
func nodeCloseReason(err error) string {
	var closed websocket.CloseError
	if errors.As(err, &closed) {
		return closed.Reason
	}

	return ""
}

// closeReason fits a reason into a WebSocket close frame, which holds 123 bytes.
func closeReason(reason string) string {
	const limit = 120
	if len(reason) > limit {
		return reason[:limit]
	}

	return reason
}

func queryInt(r *http.Request, name string, fallback int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return fallback
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}

	return n
}
