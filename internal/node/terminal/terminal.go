// Package terminal keeps the shells the owner opens on this machine from
// the web (issue #116).
//
// A session is a shell on a pseudo-terminal that outlives the connection
// watching it, after Coder's reconnecting PTY with its buffered backend
// (agent/reconnectingpty; read for how it behaves, not copied): the shell's
// output goes to a ring of the last 64 KiB, which a connection that attaches
// is sent whole before it follows the shell live, and a session nobody
// watches for five minutes is ended. The shell is the node user's own,
// started with creack/pty as in Coder's and Nezha's agents.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

const (
	// BufferBytes is how much of a shell's latest output a returning
	// connection is sent (Coder buffered.go: 64 << 10).
	BufferBytes = 64 << 10
	// IdleTimeout is how long a shell lives with no connection (Coder
	// reconnectingpty.go: 5 minutes).
	IdleTimeout = 5 * time.Minute
	// writeTimeout bounds one write to a connection, so a connection that
	// stopped reading cannot hold the shell's output.
	writeTimeout = 10 * time.Second
)

// Reasons a connection is closed with.
const (
	ReasonShellExited = "the shell ended"
	ReasonReplaced    = "another connection took this session over"
)

// ErrUnsupported says this build cannot start a shell on a pseudo-terminal.
var ErrUnsupported = errors.New("a terminal is not supported on this system")

// Shell is a shell on a pseudo-terminal: reading gives its output, writing
// is typing, Close ends it.
type Shell interface {
	io.ReadWriteCloser
	Resize(rows, cols uint16) error
}

// Conn is the connection that watches a session. Its binary messages are
// the terminal's bytes; a text message from the other end is a Resize.
type Conn interface {
	// Read returns the next message and whether it is text.
	Read(ctx context.Context) (text bool, data []byte, err error)
	// Write sends the shell's output.
	Write(ctx context.Context, data []byte) error
	// Close ends the connection with a reason for the other end.
	Close(reason string)
}

// Resize is the text message that changes the terminal's size.
type Resize = nodeproto.TerminalResize

// Options are a Manager's limits and how it starts a shell; zero fields take
// the defaults.
type Options struct {
	IdleTimeout time.Duration
	BufferBytes int
	// Start starts a shell of the given size.
	Start func(rows, cols uint16) (Shell, error)
}

// Manager holds this node's sessions by id.
type Manager struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*session
}

// New returns a Manager.
func New(opts Options) *Manager {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = IdleTimeout
	}

	if opts.BufferBytes <= 0 {
		opts.BufferBytes = BufferBytes
	}

	if opts.Start == nil {
		opts.Start = StartShell
	}

	return &Manager{opts: opts, sessions: map[string]*session{}}
}

// session is one shell and whatever watches it now.
type session struct {
	m     *Manager
	id    string
	shell Shell

	mu   sync.Mutex
	ring []byte
	conn Conn
	idle *time.Timer
	done bool
}

// ValidSize says whether a terminal may have this size.
func ValidSize(rows, cols int) bool { return nodeproto.ValidTerminalSize(rows, cols) }

// Attach connects conn to the session id, starting its shell if there is
// none yet, and serves it until the connection or the shell ends. A session
// that already has a connection gives it up to this one.
func (m *Manager) Attach(ctx context.Context, id string, rows, cols int, conn Conn) error {
	if !ValidSize(rows, cols) {
		return fmt.Errorf("a terminal of %d rows and %d columns is not allowed", rows, cols)
	}

	// The shell is not the request's: it outlives the connection that started it.
	//nolint:gosec,contextcheck // G115: ValidSize bounds both
	s, err := m.session(id, uint16(rows), uint16(cols))
	if err != nil {
		return err
	}

	if !s.attach(ctx, conn, uint16(rows), uint16(cols)) { //nolint:gosec // G115: ValidSize bounds both
		conn.Close(ReasonShellExited)

		return nil
	}

	defer s.detach(conn)

	for {
		text, data, err := conn.Read(ctx)
		if err != nil {
			return nil // the connection ended, which is how a watcher leaves
		}

		if !text {
			if _, err := s.shell.Write(data); err != nil {
				return nil // the shell ended; its reader closes the session
			}

			continue
		}

		var size Resize
		if json.Unmarshal(data, &size) == nil && ValidSize(size.Height, size.Width) {
			_ = s.shell.Resize(uint16(size.Height), uint16(size.Width)) //nolint:gosec // G115: ValidSize bounds both
		}
	}
}

// session returns the session id, starting a shell for a new one.
func (m *Manager) session(id string, rows, cols uint16) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[id]; ok {
		return s, nil
	}

	shell, err := m.opts.Start(rows, cols)
	if err != nil {
		return nil, fmt.Errorf("starting a shell: %w", err)
	}

	s := &session{m: m, id: id, shell: shell}
	m.sessions[id] = s

	go s.pump()

	return s, nil
}

// pump copies the shell's output to the ring and to the connection, until
// the shell ends.
func (s *session) pump() {
	buf := make([]byte, 32<<10)

	for {
		n, err := s.shell.Read(buf)
		if n > 0 {
			s.output(buf[:n])
		}

		if err != nil {
			s.end()

			return
		}
	}
}

func (s *session) output(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ring = append(s.ring, data...)
	if over := len(s.ring) - s.m.opts.BufferBytes; over > 0 {
		s.ring = append(s.ring[:0], s.ring[over:]...)
	}

	if s.conn == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	if err := s.conn.Write(ctx, data); err != nil {
		// A connection that cannot be written to is gone; its reader notices and detaches.
		s.conn.Close("the connection stopped reading")
	}
}

// attach makes conn the session's connection and sends it the ring. It
// reports false when the shell has already ended.
func (s *session) attach(ctx context.Context, conn Conn, rows, cols uint16) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		return false
	}

	if s.conn != nil {
		s.conn.Close(ReasonReplaced)
	}

	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}

	s.conn = conn
	_ = s.shell.Resize(rows, cols)

	if len(s.ring) > 0 {
		writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()

		_ = conn.Write(writeCtx, s.ring)
	}

	return true
}

// detach leaves the shell running with nothing watching, to be ended after
// the idle timeout unless a connection comes back.
func (s *session) detach(conn Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != conn || s.done {
		return
	}

	s.conn = nil
	s.idle = time.AfterFunc(s.m.opts.IdleTimeout, func() { _ = s.shell.Close() })
}

// end is the shell's end: the session is forgotten and its connection told.
func (s *session) end() {
	s.m.mu.Lock()
	delete(s.m.sessions, s.id)
	s.m.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.done = true

	if s.idle != nil {
		s.idle.Stop()
	}

	if s.conn != nil {
		s.conn.Close(ReasonShellExited)
	}

	_ = s.shell.Close()
}

// Close ends every shell, when the node stops.
func (m *Manager) Close() {
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))

	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		_ = s.shell.Close()
	}
}

// Count is how many shells run, for tests and the node's own log.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sessions)
}
