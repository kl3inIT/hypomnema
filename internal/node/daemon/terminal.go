package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/node/terminal"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// A terminal (issue #116) joins as in Nezha (agent terminal_session.go): the
// control plane asks on the connection the node already holds, and the node
// opens a second connection for the terminal's bytes, naming the stream it
// was given. That connection is not the session's: the shell stays when it
// drops, and the next request attaches to it again.

// openTerminal answers a terminal_open: it dials the terminal's connection
// and serves the session on it until either ends. It outlives the socket it
// was asked on, so it runs on the daemon's own context.
func (d *Daemon) openTerminal(payload json.RawMessage) error {
	var open nodeproto.TerminalOpen
	if err := json.Unmarshal(payload, &open); err != nil {
		return fmt.Errorf("decoding a terminal request: %w", err)
	}

	if d.cfg.Terminal == nil || d.base == nil {
		return nil
	}

	go func() {
		ctx := d.base

		if err := d.serveTerminal(ctx, open); err != nil && ctx.Err() == nil {
			d.logger.WarnContext(ctx, "terminal ended",
				slog.String("event", "node.terminal_failed"), slog.String("session_id", open.SessionID),
				slog.String("error", err.Error()))
		}
	}()

	return nil
}

func (d *Daemon) serveTerminal(ctx context.Context, open nodeproto.TerminalOpen) error {
	target := "ws" + strings.TrimPrefix(strings.TrimSuffix(d.cfg.URL, "/"), "http") + nodeproto.PathTerminal +
		"?stream=" + url.QueryEscape(open.StreamID)

	dialCtx, cancel := context.WithTimeout(ctx, nodeproto.TerminalMeetWithin)
	defer cancel()

	conn, resp, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{
		HTTPClient: d.http, HTTPHeader: http.Header{"Authorization": {"Bearer " + d.cfg.Token}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		return fmt.Errorf("opening the terminal's connection: %w", err)
	}

	conn.SetReadLimit(nodeproto.MaxMessageBytes)

	d.logger.InfoContext(ctx, "terminal attached",
		slog.String("event", "node.terminal_attached"), slog.String("session_id", open.SessionID))

	tc := &terminalConn{conn: conn}
	defer tc.Close("the node closed the terminal")

	return d.cfg.Terminal.Attach(ctx, open.SessionID, open.Rows, open.Cols, tc)
}

// terminalConn is the terminal's WebSocket as a session sees it.
type terminalConn struct {
	conn *websocket.Conn
}

func (c *terminalConn) Read(ctx context.Context) (bool, []byte, error) {
	kind, data, err := c.conn.Read(ctx)

	return kind == websocket.MessageText, data, err
}

func (c *terminalConn) Write(ctx context.Context, data []byte) error {
	return c.conn.Write(ctx, websocket.MessageBinary, data)
}

func (c *terminalConn) Close(reason string) {
	_ = c.conn.Close(websocket.StatusNormalClosure, reason)
}

var _ terminal.Conn = (*terminalConn)(nil)
