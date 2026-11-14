package nodeapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A terminal's connection to a node (issue #116), as Nezha's IOStream
// (service/rpc/io_stream.go): the control plane names a stream and asks the
// node on the socket it already holds; the node opens a second connection
// and presents the stream; both ends must meet within ten seconds.

// NodeEnd is the node's side of one terminal connection. Its binary
// messages are the terminal's bytes; a text message to it is a resize.
type NodeEnd struct {
	conn *websocket.Conn
	done chan struct{}
}

// Read returns the shell's next output.
func (e *NodeEnd) Read(ctx context.Context) ([]byte, error) {
	_, data, err := e.conn.Read(ctx)

	return data, err
}

// Type sends what was typed.
func (e *NodeEnd) Type(ctx context.Context, data []byte) error {
	return e.conn.Write(ctx, websocket.MessageBinary, data)
}

// Resize tells the node the terminal's new size, as JSON.
func (e *NodeEnd) Resize(ctx context.Context, size []byte) error {
	return e.conn.Write(ctx, websocket.MessageText, size)
}

// Close ends the connection and lets the node's request return.
func (e *NodeEnd) Close(reason string) {
	_ = e.conn.Close(websocket.StatusNormalClosure, reason)

	select {
	case <-e.done:
	default:
		close(e.done)
	}
}

// pendingStream is a stream the node has been asked to open.
type pendingStream struct {
	nodeID  string
	arrived chan *NodeEnd
}

// OpenTerminal asks a node to connect a terminal session and returns its end
// once the node has. A node that is not connected, or does not come within
// nodeproto.TerminalMeetWithin, is ErrUnavailable.
func (s *Server) OpenTerminal(ctx context.Context, nodeID, sessionID string, rows, cols int) (*NodeEnd, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("naming a terminal stream: %w", err)
	}

	streamID := hex.EncodeToString(raw)
	pending := &pendingStream{nodeID: nodeID, arrived: make(chan *NodeEnd, 1)}

	s.mu.Lock()
	sess := s.sessions[nodeID]
	if sess != nil {
		s.streams[streamID] = pending
	}
	s.mu.Unlock()

	if sess == nil {
		return nil, fmt.Errorf("%w: node %s is not connected", orchestration.ErrUnavailable, nodeID)
	}

	defer func() {
		s.mu.Lock()
		delete(s.streams, streamID)
		s.mu.Unlock()
	}()

	if err := sess.send(ctx, nodeproto.TypeTerminalOpen, nodeproto.TerminalOpen{
		StreamID: streamID, SessionID: sessionID, Rows: rows, Cols: cols,
	}); err != nil {
		return nil, err
	}

	select {
	case end := <-pending.arrived:
		return end, nil
	case <-time.After(nodeproto.TerminalMeetWithin):
		return nil, fmt.Errorf("%w: node %s did not open the terminal within %s; it may be a version without one",
			orchestration.ErrUnavailable, nodeID, nodeproto.TerminalMeetWithin)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// terminal is the node's request for one terminal connection: it presents
// the stream it was asked to open and stays until that connection ends.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	streamID := r.URL.Query().Get("stream")

	s.mu.Lock()
	pending := s.streams[streamID]
	if pending != nil && pending.nodeID == n.ID {
		delete(s.streams, streamID)
	} else {
		pending = nil
	}
	s.mu.Unlock()

	// A stream nobody waits for, or another node's: there is nothing to connect.
	if pending == nil {
		http.Error(w, "unknown terminal stream", http.StatusNotFound)

		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(nodeproto.MaxMessageBytes)

	end := &NodeEnd{conn: conn, done: make(chan struct{})}
	pending.arrived <- end

	select {
	case <-end.done:
	case <-r.Context().Done():
		_ = conn.Close(websocket.StatusGoingAway, "the control plane is stopping")
	}

	s.logger.DebugContext(r.Context(), "terminal stream ended",
		slog.String("event", "nodeapi.terminal_ended"), slog.String("node_id", n.ID))
}
