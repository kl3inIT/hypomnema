package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The test binary plays three parts: the tests, the shim (`shim --dir D`),
// and a fake CLI (FAKE_CLI set) that speaks a protocol of its own.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shim" && os.Args[2] == "--dir" {
		if err := shim.Main(context.Background(), os.Args[3]); err != nil {
			os.Exit(2)
		}

		os.Exit(0)
	}

	if mode := os.Getenv("FAKE_CLI"); mode != "" {
		os.Exit(fakeCLI(mode))
	}

	os.Exit(m.Run())
}

// line is a line of the fake CLI, either way.
type line struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	N       int    `json:"n,omitempty"`
}

// fakeCLI starts on "go" and says a first note; "more" makes it say the
// next; "interrupt" ends it, as does the end of its input. In mode ask it
// asks through the bridge, finishes with the answer, and ends.
func fakeCLI(mode string) int {
	out := json.NewEncoder(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	notes := 0

	say := func(l line) {
		if err := out.Encode(l); err != nil {
			os.Exit(4)
		}
	}

	for in.Scan() {
		var l line
		if json.Unmarshal(in.Bytes(), &l) != nil {
			continue
		}

		switch l.Type {
		case "go":
			say(line{Type: "started", Session: "ses_fake"})

			notes++
			say(line{Type: "note", N: notes})

			if mode == "ask" {
				return ask(say)
			}
		case "more":
			notes++
			say(line{Type: "note", N: notes})
		case "interrupt":
			return 1
		}
	}

	return 0
}

func ask(say func(line)) int {
	dir := os.Getenv("FAKE_BRIDGE")

	seq, err := bridge.Ask(dir, nodeproto.Ask{
		Text: "goth or hand-written?", Options: []nodeproto.Option{{Label: "goth"}, {Label: "hand-written"}}, To: "owner",
	})
	if err != nil {
		return 5
	}

	for {
		answer, ok, _ := bridge.Answered(dir, seq)
		if ok {
			_ = os.WriteFile("answer.txt", []byte(answer), 0o600) //nolint:gosec // a fixed name in the cwd

			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	_ = bridge.Finish(dir, bridge.Final{Summary: "added the button"})
	say(line{Type: "result"})

	return 0
}

// fake is a Runtime for the fake CLI.
type fake struct {
	exe  string
	mode string
	// turns are the turns made, the adopted one included.
	mu    sync.Mutex
	turns []*fakeTurn
}

func (f *fake) Prepare(context.Context, statedb.Dispatch) (Launch, error) {
	return Launch{
		Format: "fake",
		Write: func(dir string, _ []string) (shim.Spec, error) {
			return shim.Spec{
				Argv: []string{f.exe}, Env: append(os.Environ(), "FAKE_CLI="+f.mode, "FAKE_BRIDGE="+bridge.Dir(dir)),
			}, nil
		},
		First: [][]byte{[]byte(`{"type":"go"}`)},
		Turn:  f.turn(),
	}, nil
}

func (f *fake) Adopted() Turn { return f.turn() }

func (f *fake) turn() *fakeTurn {
	f.mu.Lock()
	defer f.mu.Unlock()

	t := &fakeTurn{}
	f.turns = append(f.turns, t)

	return t
}

func (f *fake) last() *fakeTurn {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.turns[len(f.turns)-1]
}

type fakeTurn struct {
	mu    sync.Mutex
	lines []line
	done  bool
}

func (t *fakeTurn) Line(ctx context.Context, w *Worker, raw []byte) {
	var l line
	if json.Unmarshal(raw, &l) != nil {
		return
	}

	t.mu.Lock()
	t.lines = append(t.lines, l)
	t.done = t.done || l.Type == "result"
	t.mu.Unlock()

	if l.Type == "started" {
		w.Started(ctx, l.Session)
	}
}

func (t *fakeTurn) Interrupt() []byte { return []byte(`{"type":"interrupt"}`) }

func (t *fakeTurn) Result() Result {
	t.mu.Lock()
	defer t.mu.Unlock()

	return Result{Done: t.done}
}

func (t *fakeTurn) Outcome(ex nodeproto.Exit) nodeproto.Finished {
	return nodeproto.Finished{Class: "worker_error", Exit: ex}
}

func (t *fakeTurn) seen() []line {
	t.mu.Lock()
	defer t.mu.Unlock()

	return append([]line(nil), t.lines...)
}

// reporter records what the Supervisor reports.
type reporter struct {
	mu       sync.Mutex
	started  []string
	asks     []nodeproto.Ask
	received []string
	finished []nodeproto.Finished
	exited   int
	stopped  int
	done     chan struct{}
	once     sync.Once
}

func newReporter() *reporter { return &reporter{done: make(chan struct{})} }

func (r *reporter) end() { r.once.Do(func() { close(r.done) }) }

func (r *reporter) ID() string { return "dsp_1" }

func (r *reporter) Started(_ context.Context, _, session string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.started = append(r.started, session)

	return nil
}

func (r *reporter) startedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.started)
}

func (r *reporter) Progress(context.Context) error { return nil }

func (r *reporter) Ask(_ context.Context, a nodeproto.Ask) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.asks = append(r.asks, a)

	return "rep_q", nil
}

func (r *reporter) asked() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.asks)
}

func (r *reporter) AnswerReceived(_ context.Context, questionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.received = append(r.received, questionID)

	return nil
}

func (r *reporter) Finished(_ context.Context, f nodeproto.Finished) error {
	r.mu.Lock()
	r.finished = append(r.finished, f)
	r.mu.Unlock()
	r.end()

	return nil
}

func (r *reporter) Exited(context.Context) error {
	r.mu.Lock()
	r.exited++
	r.mu.Unlock()
	r.end()

	return nil
}

func (r *reporter) Stopped(context.Context) error {
	r.mu.Lock()
	r.stopped++
	r.mu.Unlock()
	r.end()

	return nil
}

func (r *reporter) wait(t *testing.T) {
	t.Helper()

	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
		t.Fatal("the worker did not end")
	}
}

type rig struct {
	t        *testing.T
	sup      *Supervisor
	rt       *fake
	worktree string
}

func newRig(t *testing.T, mode string) *rig {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	return &rig{
		t: t, worktree: filepath.Join(t.TempDir(), "wt"),
		sup: &Supervisor{Home: t.TempDir(), Exe: exe, Logger: slog.New(slog.DiscardHandler)},
		rt:  &fake{exe: exe, mode: mode},
	}
}

func (g *rig) dispatch() statedb.Dispatch {
	d := offered()
	d.Offer.Worktree = g.worktree

	return d
}

// start starts a worker and waits until it is reported started.
func (g *rig) start(r *reporter) {
	g.t.Helper()

	require.NoError(g.t, g.sup.Start(g.t.Context(), g.rt, g.dispatch(), r))
	require.Eventually(g.t, func() bool { return r.startedCount() == 1 }, 20*time.Second, 20*time.Millisecond)
}

// restart plays a new daemon: the old one goes away without touching the
// worker, and a new Supervisor and runtime know nothing of it.
func (g *rig) restart() *rig {
	g.t.Helper()

	g.sup.drop("dsp_1").cancel()

	return &rig{
		t: g.t, worktree: g.worktree, rt: &fake{exe: g.rt.exe, mode: g.rt.mode},
		sup: &Supervisor{Home: g.sup.Home, Exe: g.sup.Exe, Logger: g.sup.Logger},
	}
}

// Scenario: the daemon restarts while the worker waits. A new Supervisor
// adopts it from its journal, and a stop through the new one interrupts it
// and is confirmed.
func TestAdoptedWorkerCanBeStopped(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	g.start(newReporter())

	again := g.restart()
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))

	again.sup.Stop("dsp_1")
	second.wait(t)

	assert.Equal(t, 1, second.stopped)
	assert.Empty(t, second.started, "the adopted worker was already reported started")
}

// Scenario: the daemon restarts after it read the start of the journal. The
// new Supervisor reads on from the saved offset: a line read before the
// restart is not handled again, and the next one is.
func TestTheJournalIsReadOnceAcrossARestart(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	g.start(newReporter())

	dir := g.sup.dir("dsp_1")
	require.Eventually(t, func() bool {
		info, err := os.Stat(filepath.Join(dir, shim.JournalFile))

		return err == nil && len(g.rt.last().seen()) == 2 && readOffset(dir) == info.Size()
	}, 20*time.Second, 20*time.Millisecond, "the first two lines are read and the offset saved")

	again := g.restart()
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))
	require.NoError(t, shim.Send(t.Context(), dir, []byte(`{"type":"more"}`)))

	adopted := again.rt.last()
	require.Eventually(t, func() bool { return len(adopted.seen()) > 0 }, 20*time.Second, 20*time.Millisecond)
	assert.Equal(t, []line{{Type: "note", N: 2}}, adopted.seen())

	again.sup.Stop("dsp_1")
	second.wait(t)
}

// Scenario: the control plane takes back a Dispatch whose worker no daemon
// follows, as after a restart. Release kills the shim found by its PID file
// and removes the run directory.
func TestReleaseKillsAWorkerNoDaemonFollows(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a node runs on Linux: on Windows the shim's worker is in no group to kill")
	}

	g := newRig(t, "hang")
	g.start(newReporter())

	dir := g.sup.dir("dsp_1")
	pid, err := shim.PID(dir)
	require.NoError(t, err)

	g.restart().sup.Release("dsp_1")

	assert.NoDirExists(t, dir)
	assert.Eventually(t, func() bool { return !shim.Alive(pid) }, 20*time.Second, 20*time.Millisecond)
}

// Scenario: the worker asks the owner through its tool. The question is
// reported, the control plane's id is recorded, the answer reaches the
// waiting tool with the option's label, and it is acknowledged.
func TestAskIsRelayedAndAnswered(t *testing.T) {
	t.Parallel()

	g := newRig(t, "ask")
	g.sup.URL = "https://hypomnema.example"

	d := g.dispatch()
	d.Offer.AgentKey = "the-dispatch's-key"

	r := newReporter()
	require.NoError(t, g.sup.Start(t.Context(), g.rt, d, r))

	require.Eventually(t, func() bool { return r.asked() == 1 }, 20*time.Second, 20*time.Millisecond)
	assert.Equal(t, "goth or hand-written?", r.asks[0].Text)

	// The tools server finds the control plane's agent API and the Dispatch's key where it finds the rest: with
	// them it reaches the shared browser (issue #212).
	agent, ok, err := bridge.AgentOf(bridge.Dir(g.sup.dir("dsp_1")))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, bridge.Agent{URL: "https://hypomnema.example", Key: "the-dispatch's-key"}, agent)

	g.sup.Asked("dsp_1", "rep_q", "q_1")

	second := 1
	g.sup.Answer("dsp_1", nodeproto.Question{
		ID: "q_1", Options: []nodeproto.Option{{Label: "goth"}, {Label: "hand-written"}},
		Answer: &nodeproto.Answer{Option: &second, Text: "keep it small"},
	})
	r.wait(t)

	answer, err := os.ReadFile(filepath.Join(g.worktree, "answer.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hand-written keep it small", string(answer))
	assert.Equal(t, []string{"q_1"}, r.received)
	require.Len(t, r.finished, 1)
	assert.Equal(t, "added the button", r.finished[0].Summary, "the outcome is the finish tool's report")
	assert.Equal(t, 1, r.asked(), "a question is asked once")
}
