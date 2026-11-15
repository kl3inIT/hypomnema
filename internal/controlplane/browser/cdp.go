package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// A connection of the engine's own to Chrome's DevTools protocol, beside the
// ones agent-browser holds: the identity's sidecar (sidecar.go) and the read
// of the identity (identity.go) use it, as OpenMausBot's sidecar holds a
// connection of its own (server/browser/chrome/sidecar.ts). Commands are
// numbered and answered by number; what Chrome sends unasked is an event.

// maxCDPMessage bounds one message from Chrome; the engine asks for nothing large.
const maxCDPMessage = 16 << 20

// errCDPClosed reports a connection that ended before its answer came.
var errCDPClosed = errors.New("the DevTools connection closed")

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// cdpEvent is what Chrome sent unasked; SessionID is the session it came on.
type cdpEvent struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

type cdpConn struct {
	ws     *websocket.Conn
	stop   context.CancelFunc
	closed chan struct{}

	mu      sync.Mutex
	next    int64
	waiting map[int64]chan cdpMessage
}

// dialCDP connects to a DevTools address. events is called for each event on
// the connection's own goroutine: it must not wait for an answer there.
func dialCDP(ctx context.Context, address string, events func(cdpEvent)) (*cdpConn, error) {
	// No Origin is sent: Chrome takes a connection without one from a program, not a page.
	//nolint:bodyclose // A WebSocket's response has no body left to close.
	ws, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		return nil, fmt.Errorf("reaching Chrome's DevTools: %w", err)
	}

	ws.SetReadLimit(maxCDPMessage)

	// The connection outlives the request that opened it.
	reading, stop := context.WithCancel(context.WithoutCancel(ctx))
	c := &cdpConn{ws: ws, stop: stop, closed: make(chan struct{}), waiting: map[int64]chan cdpMessage{}}

	go c.read(reading, events)

	return c, nil
}

func (c *cdpConn) read(ctx context.Context, events func(cdpEvent)) {
	defer func() {
		c.mu.Lock()
		for id, answer := range c.waiting {
			close(answer)
			delete(c.waiting, id)
		}

		c.waiting = nil
		c.mu.Unlock()

		close(c.closed)
	}()

	for {
		_, raw, err := c.ws.Read(ctx)
		if err != nil {
			return
		}

		var m cdpMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}

		if m.ID == 0 {
			if m.Method != "" && events != nil {
				events(cdpEvent{Method: m.Method, SessionID: m.SessionID, Params: m.Params})
			}

			continue
		}

		c.mu.Lock()
		answer, ok := c.waiting[m.ID]
		delete(c.waiting, m.ID)
		c.mu.Unlock()

		if ok {
			answer <- m
		}
	}
}

// start writes a command and returns what waits for its answer. Commands
// reach Chrome in the order they were started.
func (c *cdpConn) start(
	ctx context.Context, method string, params any, session string,
) (func(context.Context, any) error, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()

	if c.waiting == nil {
		c.mu.Unlock()

		return nil, errCDPClosed
	}

	c.next++
	id := c.next
	answer := make(chan cdpMessage, 1)
	c.waiting[id] = answer

	out, err := json.Marshal(cdpMessage{ID: id, Method: method, Params: raw, SessionID: session})
	if err == nil {
		// Under the lock, so that commands go out in the order they were numbered.
		err = c.ws.Write(ctx, websocket.MessageText, out)
	}

	if err != nil {
		delete(c.waiting, id)
	}

	c.mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}

	return func(ctx context.Context, result any) error {
		select {
		case m, ok := <-answer:
			switch {
			case !ok:
				return fmt.Errorf("%s: %w", method, errCDPClosed)
			case m.Error != nil:
				return fmt.Errorf("%s: %s", method, m.Error.Message)
			case result != nil && len(m.Result) > 0:
				return json.Unmarshal(m.Result, result)
			}

			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

// send runs a command and waits for its answer, which goes into result when
// that is not nil.
func (c *cdpConn) send(ctx context.Context, method string, params any, session string, result any) error {
	wait, err := c.start(ctx, method, params, session)
	if err != nil {
		return err
	}

	return wait(ctx, result)
}

// close ends the connection.
func (c *cdpConn) close() {
	c.stop()
	_ = c.ws.Close(websocket.StatusNormalClosure, "")

	<-c.closed
}
