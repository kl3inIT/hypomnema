package terminal_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/terminal"
)

// fakeShell is a shell that prints what the test tells it to and keeps what
// is typed and the sizes it was given.
type fakeShell struct {
	out    chan []byte
	closed chan struct{}
	once   sync.Once

	mu    sync.Mutex
	typed strings.Builder
	sizes [][2]uint16
}

func newFakeShell() *fakeShell {
	return &fakeShell{out: make(chan []byte, 16), closed: make(chan struct{})}
}

func (s *fakeShell) Read(p []byte) (int, error) {
	select {
	case data := <-s.out:
		return copy(p, data), nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *fakeShell) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.typed.Write(p)
}

func (s *fakeShell) Resize(rows, cols uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sizes = append(s.sizes, [2]uint16{rows, cols})

	return nil
}

func (s *fakeShell) Close() error {
	s.once.Do(func() { close(s.closed) })

	return nil
}

func (s *fakeShell) print(text string) { s.out <- []byte(text) }

func (s *fakeShell) wasTyped() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.typed.String()
}

func (s *fakeShell) lastSize() [2]uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sizes[len(s.sizes)-1]
}

type message struct {
	text bool
	data []byte
}

// fakeConn is a browser's end: the test sends through in, and reads what
// the session wrote.
type fakeConn struct {
	in     chan message
	closed chan struct{}
	once   sync.Once

	mu     sync.Mutex
	shown  strings.Builder
	reason string
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan message, 16), closed: make(chan struct{})}
}

func (c *fakeConn) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case m := <-c.in:
		return m.text, m.data, nil
	case <-c.closed:
		return false, nil, errors.New("closed")
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
}

func (c *fakeConn) Write(_ context.Context, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.shown.Write(data)

	return nil
}

func (c *fakeConn) Close(reason string) {
	c.once.Do(func() {
		c.mu.Lock()
		c.reason = reason
		c.mu.Unlock()
		close(c.closed)
	})
}

func (c *fakeConn) screen() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.shown.String()
}

func (c *fakeConn) closedWith() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reason
}

type world struct {
	t      *testing.T
	m      *terminal.Manager
	shells []*fakeShell
	mu     sync.Mutex
}

func newWorld(t *testing.T, opts terminal.Options) *world {
	t.Helper()

	w := &world{t: t}
	opts.Start = func(uint16, uint16) (terminal.Shell, error) {
		w.mu.Lock()
		defer w.mu.Unlock()

		s := newFakeShell()
		w.shells = append(w.shells, s)

		return s, nil
	}
	w.m = terminal.New(opts)
	t.Cleanup(w.m.Close)

	return w
}

// attach watches the session from a new connection and returns it with a
// channel that closes when the session stops serving it.
func (w *world) attach(id string, rows, cols int) (*fakeConn, <-chan struct{}) {
	w.t.Helper()

	conn, done := newFakeConn(), make(chan struct{})

	go func() {
		defer close(done)

		_ = w.m.Attach(w.t.Context(), id, rows, cols, conn)
	}()

	return conn, done
}

func (w *world) shell(i int) *fakeShell {
	w.t.Helper()

	require.Eventually(w.t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()

		return len(w.shells) > i
	}, time.Second, 5*time.Millisecond)

	w.mu.Lock()
	defer w.mu.Unlock()

	return w.shells[i]
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 2*time.Second, 5*time.Millisecond)
}

// Scenario: the owner opens a terminal, types, and changes the window's
// size. What is typed reaches the shell, what the shell prints reaches the
// page, and the shell is told the new size; a size no terminal has is
// ignored.
func TestTypingAndOutputPassThroughASession(t *testing.T) {
	t.Parallel()

	w := newWorld(t, terminal.Options{})
	conn, _ := w.attach("s1", 24, 80)
	shell := w.shell(0)

	conn.in <- message{data: []byte("ls\n")}

	eventually(t, func() bool { return shell.wasTyped() == "ls\n" })

	shell.print("README.md\n")
	eventually(t, func() bool { return conn.screen() == "README.md\n" })
	assert.Equal(t, [2]uint16{24, 80}, shell.lastSize())

	resize, err := json.Marshal(terminal.Resize{Height: 40, Width: 120})
	require.NoError(t, err)
	conn.in <- message{text: true, data: resize}

	eventually(t, func() bool { return shell.lastSize() == [2]uint16{40, 120} })

	conn.in <- message{text: true, data: []byte(`{"height":40000,"width":1}`)}
	conn.in <- message{data: []byte("x")}

	eventually(t, func() bool { return shell.wasTyped() == "ls\nx" })
	assert.Equal(t, [2]uint16{40, 120}, shell.lastSize())
}

// Scenario: the page is reloaded while a command still prints. The shell
// goes on with nobody watching; the page that comes back with the same
// session is first shown what was printed meanwhile, then follows live, in
// the one shell that was never restarted.
func TestSessionOutlivesItsConnectionAndReplaysWhatWasMissed(t *testing.T) {
	t.Parallel()

	w := newWorld(t, terminal.Options{})
	first, gone := w.attach("s1", 24, 80)
	shell := w.shell(0)

	shell.print("step 1\n")
	eventually(t, func() bool { return first.screen() == "step 1\n" })

	first.Close("reload")
	<-gone

	shell.print("step 2\n")

	var back *fakeConn

	eventually(t, func() bool {
		if back == nil {
			back, _ = w.attach("s1", 30, 100)
		}

		return back.screen() == "step 1\nstep 2\n"
	})

	shell.print("step 3\n")
	eventually(t, func() bool { return back.screen() == "step 1\nstep 2\nstep 3\n" })
	assert.Equal(t, 1, w.m.Count())
	assert.Equal(t, [2]uint16{30, 100}, shell.lastSize())
}

// Scenario: a long-running command prints far more than is kept. A
// returning page is shown only the latest output, up to the buffer's size.
func TestReplayKeepsOnlyTheLatestOutput(t *testing.T) {
	t.Parallel()

	w := newWorld(t, terminal.Options{BufferBytes: 8})
	first, gone := w.attach("s1", 24, 80)
	shell := w.shell(0)

	shell.print("0123456789")
	shell.print("abcdef")
	eventually(t, func() bool { return first.screen() == "0123456789abcdef" })

	first.Close("reload")
	<-gone

	back, _ := w.attach("s1", 24, 80)
	eventually(t, func() bool { return back.screen() == "89abcdef" })
}

// Scenario: the owner closes the tab and does not come back. After the idle
// time the shell is ended, so nothing keeps running on the machine; a page
// that comes back later with the same session gets a new shell.
func TestUnwatchedSessionIsEndedAfterTheIdleTime(t *testing.T) {
	t.Parallel()

	w := newWorld(t, terminal.Options{IdleTimeout: 30 * time.Millisecond})
	conn, gone := w.attach("s1", 24, 80)
	shell := w.shell(0)

	conn.Close("tab closed")
	<-gone

	select {
	case <-shell.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the shell was not ended")
	}

	eventually(t, func() bool { return w.m.Count() == 0 })

	w.attach("s1", 24, 80)
	assert.NotSame(t, shell, w.shell(1))
}

// Scenario: the same session is opened in a second tab, and then the shell
// exits. The first tab is told another connection took over; the second is
// told the shell ended, and the session is gone.
func TestSecondConnectionTakesOverAndShellExitEndsTheSession(t *testing.T) {
	t.Parallel()

	w := newWorld(t, terminal.Options{})
	first, firstGone := w.attach("s1", 24, 80)
	shell := w.shell(0)

	shell.print("hello\n")
	eventually(t, func() bool { return first.screen() == "hello\n" })

	second, secondGone := w.attach("s1", 24, 80)
	<-firstGone
	assert.Equal(t, terminal.ReasonReplaced, first.closedWith())
	eventually(t, func() bool { return second.screen() == "hello\n" })

	require.NoError(t, shell.Close())
	<-secondGone
	assert.Equal(t, terminal.ReasonShellExited, second.closedWith())
	eventually(t, func() bool { return w.m.Count() == 0 })
}

// Scenario: a terminal is asked for with a size no terminal has. No shell
// is started.
func TestAttachRefusesAnImpossibleSize(t *testing.T) {
	t.Parallel()

	w := newWorld(t, terminal.Options{})

	require.Error(t, w.m.Attach(t.Context(), "s1", 0, 80, newFakeConn()))
	require.Error(t, w.m.Attach(t.Context(), "s1", 24, 5000, newFakeConn()))
	assert.Zero(t, w.m.Count())
}
