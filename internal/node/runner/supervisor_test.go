package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/memlimit"
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

	if os.Getenv("FAKE_HOG") != "" {
		hog()
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
// next, "result" its result; "interrupt" ends it, as does the end of its
// input. In mode ask it
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
			if mode == "isolated-temp" && !writeIsolatedScratch() {
				return 6
			}
			if mode == "env-temp" && !writeScratch(os.Getenv("GOTMPDIR"), "env", "value") {
				return 6
			}
			if mode == "commit" {
				commitInWorktree()
			}
			say(line{Type: "started", Session: "ses_fake"})

			if mode == "hog" {
				hog()
			}

			notes++
			say(line{Type: "note", N: notes})

			if mode == "ask" {
				return ask(say)
			}
		case "more":
			notes++
			say(line{Type: "note", N: notes})
		case "result":
			say(line{Type: "result"})
		case "finish-hog":
			// The worker reports, then a process of its takes more memory than its scope allows and is killed,
			// and the worker ends at once, before anything reads its scope again.
			_ = bridge.Finish(os.Getenv("FAKE_BRIDGE"), bridge.Final{Summary: "the tests pass"})
			child := exec.Command(os.Args[0]) //nolint:noctx,gosec // the fake CLI's own child
			child.Env = append(os.Environ(), "FAKE_HOG=1")
			_ = child.Run()
			say(line{Type: "result"})

			return 0
		case "interrupt":
			// The session the interrupt named, for a test to read.
			_ = os.WriteFile("interrupted.txt", []byte(l.Session), 0o600)

			return 1
		}
	}

	return 0
}

// hog takes 2 GiB, a page at a time, as a test binary that leaks does; the kernel kills it at its slice's limit.
func hog() {
	const page = 4096

	b := make([]byte, 2<<30)
	for i := 0; i < len(b); i += page {
		b[i] = 1
	}
}

// commitInWorktree commits in the worker's directory, its worktree, as a worker's git does, with the environment
// the worker was started with; what git said when it failed is left in commit-failed.txt.
func commitInWorktree() {
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", "worker") //nolint:noctx // the fake worker's own
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.WriteFile("commit-failed.txt", append(out, err.Error()...), 0o600)
	}
}

func writeIsolatedScratch() bool {
	temp, err := os.Stat(os.Getenv("TMPDIR")) //nolint:gosec // the supervisor sets this to the fixture's scratch
	if err != nil {
		return false
	}
	for _, dir := range []string{"/tmp", "/var/tmp"} {
		bound, err := os.Stat(dir)
		if err != nil || !os.SameFile(temp, bound) {
			return false
		}
	}
	return writeScratch("/tmp", "a", "x\n") && writeScratch("/var/tmp", "b", "y\n")
}

func writeScratch(dir, name, value string) bool {
	return dir != "" && os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600) == nil //nolint:gosec // test scratch
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
		// The git host's environment comes after the node's, as the real runtimes put it (claude, codex, acp).
		Write: func(dir string, hostEnv []string) (shim.Spec, error) {
			env := append(os.Environ(), "FAKE_CLI="+f.mode, "FAKE_BRIDGE="+bridge.Dir(dir))

			return shim.Spec{Argv: []string{f.exe}, Env: append(env, hostEnv...)}, nil
		},
		First: [][]byte{[]byte(`{"type":"go"}`)},
		Turn:  f.turn(),
	}, nil
}

func (f *fake) Adopted(state []byte) Turn {
	t := f.turn()

	var s fakeState
	if json.Unmarshal(state, &s) == nil {
		t.done, t.session = s.Done, s.Session
	}

	return t
}

// fakeState is what a fake turn keeps across a restart.
type fakeState struct {
	Done    bool   `json:"done"`
	Session string `json:"session"`
}

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
	mu      sync.Mutex
	lines   []line
	done    bool
	session string
}

func (t *fakeTurn) Line(ctx context.Context, w *Worker, raw []byte) {
	var l line
	if json.Unmarshal(raw, &l) != nil {
		return
	}

	t.mu.Lock()
	t.lines = append(t.lines, l)
	t.done = t.done || l.Type == "result"

	if l.Type == "started" {
		t.session = l.Session
	}
	t.mu.Unlock()

	if l.Type == "started" {
		w.Started(ctx, l.Session)
	}
}

func (t *fakeTurn) Interrupt() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	raw, _ := json.Marshal(line{Type: "interrupt", Session: t.session}) //nolint:errchkjson // strings

	return raw
}

type noInterruptRuntime struct{ *fake }

func (rt noInterruptRuntime) Adopted(state []byte) Turn {
	return noInterruptTurn{rt.fake.Adopted(state)}
}

type noInterruptTurn struct{ Turn }

func (noInterruptTurn) Interrupt() []byte { return nil }

func (t *fakeTurn) Result() Result {
	t.mu.Lock()
	defer t.mu.Unlock()

	return Result{Done: t.done}
}

func (t *fakeTurn) State() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	raw, _ := json.Marshal(fakeState{Done: t.done, Session: t.session}) //nolint:errchkjson // a bool and a string

	return raw
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

func (r *reporter) PublishedHead(context.Context, nodeproto.PublishedHead) error { return nil }

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

	w := g.sup.drop("dsp_1")
	w.cancel()
	<-w.done

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
	checkAdoptedWorkerCanBeStopped(t, g)
}

func checkAdoptedWorkerCanBeStopped(t *testing.T, fixture *rig) {
	t.Helper()
	fixture.start(newReporter())

	again := fixture.restart()
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))
	temp := filepath.Join(again.sup.dir("dsp_1"), "tmp")
	assert.DirExists(t, temp)
	assert.False(t, again.sup.Stopped("dsp_1", false))
	if fixture.sup.Isolation.On() {
		for name, want := range map[string]string{"a": "x\n", "b": "y\n"} {
			got, err := os.ReadFile(filepath.Join(temp, name)) //nolint:gosec // test Dispatch's scratch
			require.NoError(t, err)
			assert.Equal(t, want, string(got))
		}
	}

	again.sup.Stop("dsp_1")
	second.wait(t)

	assert.Equal(t, 1, second.stopped)
	assert.Empty(t, second.started, "the adopted worker was already reported started")
	if runtime.GOOS == "linux" {
		require.Eventually(t, func() bool { return again.sup.Stopped("dsp_1", false) }, 5*time.Second, 20*time.Millisecond)
		require.NoError(t, again.sup.CleanTemp(t.Context(), "dsp_1"))
		assert.NoDirExists(t, temp)
		assert.FileExists(t, filepath.Join(again.sup.dir("dsp_1"), shim.JournalFile))
	}
}

func TestWorkerWithoutTempIsolationUsesGoTemp(t *testing.T) {
	t.Parallel()
	fixture := newRig(t, "env-temp")
	report := newReporter()
	fixture.start(report)
	dir := fixture.sup.dir("dsp_1")
	t.Cleanup(func() { _ = shim.KillRecorded(dir) })
	assert.FileExists(t, filepath.Join(dir, "tmp", "env"))
	fixture.sup.Stop("dsp_1")
	report.wait(t)
}

func TestWorkerUsesShortConfiguredHomeForSocket(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires Unix domain sockets and a detached shim")
	}
	g := newRig(t, "hang")
	realHome := filepath.Join(g.sup.Home, strings.Repeat("long", 30))
	require.NoError(t, os.Mkdir(realHome, 0o700))
	short, err := os.MkdirTemp("", "hy-") //nolint:usetesting // The test name in t.TempDir can exceed the socket limit.
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(short)) })
	alias := filepath.Join(short, "h")
	require.NoError(t, os.Symlink(realHome, alias))
	if len(filepath.Join(alias, "run", "dsp_1", shim.SocketFile)) >= 104 {
		t.Skip("temporary base path is too long for a Unix socket")
	}
	g.sup.Home, err = filepath.EvalSymlinks(realHome)
	require.NoError(t, err)
	g.sup.SocketHome = alias
	t.Cleanup(func() { g.sup.Release("dsp_1") })
	r := newReporter()
	g.start(r)
	again := g.restart()
	again.sup.SocketHome = alias
	t.Cleanup(func() { again.sup.Release("dsp_1") })
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))
	again.sup.Stop("dsp_1")
	second.wait(t)
	assert.Equal(t, 1, second.stopped)
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

		return err == nil && len(g.rt.last().seen()) == 2 && checkpointOffset(dir) == info.Size()
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
// follows, as after a restart. Release reaches the shim at its private socket
// and removes the run directory.
func TestReleaseKillsAWorkerNoDaemonFollows(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	g.start(newReporter())

	dir := g.sup.dir("dsp_1")
	pid, err := shim.PID(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shim.KillRecorded(dir) })
	identity := filepath.Join(dir, "shim.identity")
	original, err := os.ReadFile(identity) //nolint:gosec // an owned test fixture
	require.NoError(t, err)
	// Without its socket the shim is reached only through its recorded identity, which no longer matches.
	socket, hidden := filepath.Join(dir, shim.SocketFile), filepath.Join(dir, "hidden.sock")
	require.NoError(t, os.Rename(socket, hidden))
	require.NoError(t, os.WriteFile(identity, []byte(`{"Started":1}`), 0o600))
	restarted := g.restart().sup
	restarted.Release("dsp_1")
	assert.Never(t, func() bool { return !shim.Alive(pid) }, 100*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, os.Rename(hidden, socket))
	require.NoError(t, os.WriteFile(identity, original, 0o600)) //nolint:gosec // the owned shim fixture
	alias := filepath.Join(t.TempDir(), "node-home")
	require.NoError(t, os.Symlink(restarted.Home, alias))
	restarted.Home = alias

	restarted.Release("dsp_1")

	assert.DirExists(t, dir, "the shim record is needed to confirm the worker has stopped")
	assert.Eventually(t, func() bool { return !shim.Alive(pid) }, 20*time.Second, 20*time.Millisecond)
}

func TestAnUnconfirmedExitDoesNotConfirmCancellation(t *testing.T) {
	t.Parallel()
	report := newReporter()
	sup := &Supervisor{}
	sup.ended(t.Context(), &Worker{stopping: true, r: report, turn: &fakeTurn{}}, shim.Exit{Unconfirmed: true})
	assert.Zero(t, report.stopped)
	assert.Zero(t, report.exited)
	assert.Empty(t, report.finished)
}

func TestStopRevokesShimLaunchBeforePID(t *testing.T) {
	t.Parallel()
	g := newRig(t, "hang")
	realExe := g.sup.Exe
	wrapper := filepath.Join(g.sup.Home, "delayed-shim")
	command := `#!/bin/sh
: > "$3/spawned"
while [ ! -f "$3/proceed" ]; do /bin/sleep 0.01; done
exec "` + realExe + `" "$@"
`
	require.NoError(t, os.WriteFile(wrapper, []byte(command), 0o700)) //nolint:gosec // executable fixture
	g.sup.Exe = wrapper
	dispatch := g.dispatch()
	dir := g.sup.dir(dispatch.ID)
	proceed := filepath.Join(dir, "proceed")
	t.Cleanup(func() {
		_ = os.WriteFile(proceed, nil, 0o600)
		if _, err := os.Stat(filepath.Join(dir, "spawned")); err != nil {
			return
		}
		assert.Eventually(t, func() bool {
			_, err := shim.PID(dir)
			return err == nil
		}, 5*time.Second, 10*time.Millisecond)
		_ = shim.KillRecorded(dir)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.sup.Start(ctx, g.rt, dispatch, newReporter()) }()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "spawned"))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoFileExists(t, filepath.Join(dir, shim.LaunchingFile), "the canceled Start revokes the launch")
	require.NoError(t, os.WriteFile(proceed, nil, 0o600))
	require.Eventually(t, func() bool {
		pid, err := shim.PID(dir)
		return err == nil && shim.GroupGone(pid)
	}, 5*time.Second, 10*time.Millisecond)
	assert.NoFileExists(t, filepath.Join(dir, shim.ClaimedFile))
}

func TestStopRevokesAnAdoptedLaunchBeforeShimPID(t *testing.T) {
	t.Parallel()
	g := newRig(t, "hang")
	dispatch := g.dispatch()
	dir := g.sup.dir(dispatch.ID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for _, marker := range []string{shim.PreparingFile, shim.LaunchingFile} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, marker), nil, 0o600))
	}
	require.NoError(t, g.sup.Adopt(t.Context(), noInterruptRuntime{g.rt}, dispatch, newReporter()))
	t.Cleanup(func() { g.sup.Release(dispatch.ID) })
	require.NotNil(t, g.sup.get(dispatch.ID))
	require.NoFileExists(t, filepath.Join(dir, shim.PIDFile))

	g.sup.Stop(dispatch.ID)

	require.NoFileExists(t, filepath.Join(dir, shim.LaunchingFile))
	delayed := exec.CommandContext(t.Context(), g.sup.Exe, "shim", "--dir", dir) //nolint:gosec // the test binary
	require.ErrorAs(t, delayed.Run(), new(*exec.ExitError))
	require.FileExists(t, filepath.Join(dir, shim.PIDFile))
	require.NoFileExists(t, filepath.Join(dir, shim.ClaimedFile))
}

func TestRestartReclaimsTempBeforeShimPID(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mount identity is unavailable on Windows")
	}
	sup := &Supervisor{Home: t.TempDir(), Logger: slog.New(slog.DiscardHandler)}
	dir := sup.dir("dsp_1")
	tmp := filepath.Join(dir, "tmp")
	require.NoError(t, os.MkdirAll(tmp, 0o700))
	for _, marker := range []string{shim.PreparingFile, shim.LaunchingFile} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, marker), nil, 0o600))
	}
	require.NoError(t, sup.CleanTemp(t.Context(), "dsp_1"))
	assert.NoDirExists(t, tmp)
	require.ErrorContains(t, shim.Main(t.Context(), dir), "claiming permission")
}

// Scenario: issue #1219. An offer of a control plane that keeps a question's
// several decisions lets the worker's tools take them.
func TestAControlPlaneThatKeepsDecisionsLetsTheWorkerAskThem(t *testing.T) {
	t.Parallel()

	g := newRig(t, "ask")
	d := g.dispatch()
	d.Offer.QuestionParts = true

	r := newReporter()
	t.Cleanup(func() { g.sup.Release(d.ID) })
	require.NoError(t, g.sup.Start(t.Context(), g.rt, d, r))
	require.Eventually(t, func() bool { return r.asked() == 1 }, 20*time.Second, 20*time.Millisecond)
	assert.True(t, bridge.QuestionPartsEnabled(bridge.Dir(g.sup.dir("dsp_1"))))
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
	// them it reaches the shared browser (issue #212); and the worktree, where show_artifact reads the path it is
	// given (issue #536).
	agent, ok, err := bridge.AgentOf(bridge.Dir(g.sup.dir("dsp_1")))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, bridge.Agent{URL: "https://hypomnema.example", Key: "the-dispatch's-key",
		Worktree: d.Offer.Worktree}, agent)
	assert.False(t, bridge.QuestionPartsEnabled(bridge.Dir(g.sup.dir("dsp_1"))),
		"a control plane that does not say it keeps several decisions is asked one question (issue #1219)")

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

// Scenario: the daemon restarts while the worker works. A stop through the
// new daemon names the CLI's session, which the earlier daemon read before
// the restart (issue #403).
func TestAnAdoptedWorkerIsStoppedInItsSession(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	g.start(newReporter())
	g.waitRead(2)

	again := g.restart()
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))

	again.sup.Stop("dsp_1")
	second.wait(t)

	interrupted, err := os.ReadFile(filepath.Join(g.worktree, "interrupted.txt"))
	require.NoError(t, err)
	assert.Equal(t, "ses_fake", string(interrupted))
}

// Scenario: the CLI said its result before the daemon restarted, and exits
// after. The new daemon reports the turn's outcome, not an unknown exit
// (issue #403).
func TestATurnsResultOutlivesARestart(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	g.start(newReporter())
	require.NoError(t, shim.Send(t.Context(), g.sup.dir("dsp_1"), []byte(`{"type":"result"}`)))
	g.waitRead(3)

	again := g.restart()
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))
	require.NoError(t, shim.Send(t.Context(), g.sup.dir("dsp_1"), []byte(shim.CloseLine)))
	second.wait(t)

	assert.Zero(t, second.exited, "the outcome is known")
	require.Len(t, second.finished, 1)
	assert.Equal(t, "worker_error", second.finished[0].Class, "the turn's Outcome")
}

// Scenario: a daemon stopped after it made a worker's run directory and
// before its shim started. The new daemon adopts it, waits for the shim as a
// start does, and ends it as a start that failed (issue #403).
func TestDispatchTempOverridesGoTemp(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "dispatch-temp")
	env := tempEnv([]string{"PATH=/bin", "GOTMPDIR=/tmp", "TMPDIR=/tmp"}, dir)
	assert.Equal(t, []string{"PATH=/bin", "TMPDIR=" + dir, "TEMP=" + dir, "TMP=" + dir, "GOTMPDIR=" + dir}, env)
}

func TestARunDirectoryWithoutAShimEnds(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	require.NoError(t, os.MkdirAll(g.sup.dir("dsp_1"), 0o700))

	r := newReporter()
	require.NoError(t, g.sup.Adopt(t.Context(), g.rt, g.dispatch(), r))
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Equal(t, "start_failed", r.finished[0].Class)
	assert.Equal(t, "the shim did not start", r.finished[0].Summary)
}

// Scenario: the control plane takes back a Dispatch while its journal reader
// is busy. Release returns once the reader stopped, and nothing is reported
// after it (issue #403).
func TestReleaseWaitsForTheJournalReader(t *testing.T) {
	t.Parallel()

	sup := &Supervisor{Home: t.TempDir(), Logger: slog.New(slog.DiscardHandler)}
	dir := sup.dir("dsp_1")
	_, err := bridge.Ask(bridge.Dir(dir), nodeproto.Ask{Text: "goth?", To: "owner"})
	require.NoError(t, err)

	r := &slowAsk{
		reporter: newReporter(), asking: make(chan struct{}), canceled: make(chan struct{}), gate: make(chan struct{}),
	}
	// A shim that is gone: the reader, once free, would see it dead and report.
	sup.watch(t.Context(), &Worker{id: "dsp_1", dir: dir, pid: deadPID(t), r: r, turn: &fakeTurn{}, started: true})
	waitFor(t, r.asking)

	released := make(chan struct{})

	go func() {
		sup.Release("dsp_1")
		close(released)
	}()

	waitFor(t, r.canceled)

	// An earlier Release returned at once; give the reader time to run on after it.
	select {
	case <-released:
	case <-time.After(time.Second):
	}

	close(r.gate)
	waitFor(t, released)

	assert.Never(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()

		return r.exited+len(r.finished)+r.stopped > 0
	}, time.Second, 20*time.Millisecond, "a released worker reports nothing")
	assert.DirExists(t, dir, "releasing a worker leaves its journal in place")
}

// checkpointOffset is how far the checkpoint in dir says the journal was read.
func checkpointOffset(dir string) int64 {
	n, _ := readCheckpoint(dir)

	return n
}

// Scenario: a checkpoint is read back with the offset and the turn's state
// it was written with; a missing one reads as nothing.
func TestACheckpointIsReadBack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	n, state := readCheckpoint(dir)
	assert.Zero(t, n)
	assert.Nil(t, state)

	writeCheckpoint(dir, 456, []byte(`{"done":true}`))

	n, state = readCheckpoint(dir)
	assert.Equal(t, int64(456), n)
	assert.JSONEq(t, `{"done":true}`, string(state))
}

// slowAsk is a reporter whose Ask is still running when the worker is released.
type slowAsk struct {
	*reporter

	asking, canceled, gate chan struct{}
}

func (r *slowAsk) Ask(ctx context.Context, _ nodeproto.Ask) (string, error) {
	close(r.asking)
	<-ctx.Done()
	close(r.canceled)
	<-r.gate

	return "", ctx.Err()
}

// deadPID is the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^$") //nolint:gosec // the test binary itself
	require.NoError(t, cmd.Run())

	return cmd.Process.Pid
}

func waitFor(t *testing.T, c <-chan struct{}) {
	t.Helper()

	select {
	case <-c:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out")
	}
}

// waitRead waits until the turn read n lines and the offset was saved after them.
func (g *rig) waitRead(n int) {
	g.t.Helper()

	dir := g.sup.dir("dsp_1")
	require.Eventually(g.t, func() bool {
		info, err := os.Stat(filepath.Join(dir, shim.JournalFile))

		return err == nil && len(g.rt.last().seen()) == n && checkpointOffset(dir) == info.Size()
	}, 20*time.Second, 20*time.Millisecond)
}

// Scenario: the kernel killed the worker's CLI for memory, at the node's limit, before it called finish (issue
// #1313 row 4). The Dispatch fails with the reason oom_killed named first in its summary, not an unknown outcome a
// coordinator could take for a flaky test.
func TestAWorkerKilledForMemoryEndsOOMKilled(t *testing.T) {
	t.Parallel()

	report := newReporter()
	sup := &Supervisor{}
	w := &Worker{id: "dsp_1", dir: t.TempDir(), r: report, turn: &fakeTurn{}, started: true}
	sup.ended(t.Context(), w, shim.Exit{Code: -1, Signal: "killed", OOMKills: 1})

	require.Len(t, report.finished, 1)
	assert.Zero(t, report.exited)
	f := report.finished[0]
	assert.Equal(t, "worker_error", f.Class)
	assert.Equal(t, nodeproto.Exit{Kind: "signaled", Signal: "killed", Reason: "oom_killed"}, f.Exit)
	assert.Equal(t, "oom_killed: the kernel killed a process of this worker for memory, at the limit of the node's "+
		"workers (hypomnema-workers.slice) or of its profile's limits.memory", f.Summary)

	// The same exit without a kill for memory is an unknown outcome, as before.
	plain := newReporter()
	sup.ended(t.Context(), &Worker{id: "dsp_1", dir: t.TempDir(), r: plain, turn: &fakeTurn{}, started: true},
		shim.Exit{Code: -1, Signal: "killed"})
	assert.Equal(t, 1, plain.exited)
	assert.Empty(t, plain.finished)
}

// Scenario: the kernel killed one of the worker's test binaries for memory, and the worker went on and finished
// (issue #1313 row 4, review round 1). A process killed for memory fails the Dispatch whatever the worker reported,
// as Kubernetes takes an OOMKilled container for a failure even when it exited 0 (pkg/kubelet/kuberuntime/
// helpers.go:170-177): the kill comes first in the summary, the worker's report after it, its evidence kept.
func TestAFinishedWorkerKilledForMemoryFails(t *testing.T) {
	t.Parallel()

	report := newReporter()
	dir := t.TempDir()
	require.NoError(t, bridge.Finish(bridge.Dir(dir), bridge.Final{
		Summary: "the tests pass", Evidence: []nodeproto.Evidence{{Kind: "commit", Ref: "abc"}},
	}))

	sup := &Supervisor{}
	sup.ended(t.Context(), &Worker{id: "dsp_1", dir: dir, r: report, turn: &fakeTurn{done: true}, started: true},
		shim.Exit{Code: 0, OOMKills: 2})

	require.Len(t, report.finished, 1)
	f := report.finished[0]
	assert.Equal(t, "worker_error", f.Class)
	assert.Equal(t, "oom_killed", f.Exit.Reason)
	assert.Equal(t, "oom_killed: the kernel killed 2 processes of this worker for memory, at the limit of the node's "+
		"workers (hypomnema-workers.slice) or of its profile's limits.memory\n\nthe tests pass", f.Summary)
	assert.Equal(t, []nodeproto.Evidence{{Kind: "commit", Ref: "abc"}}, f.Evidence)
	assert.Nil(t, f.Gate)
}

// Scenario: the kernel killed the worker for memory before its CLI said it started (issue #1313 row 4, review round
// 1). It ran, so it is reported started and then failed for memory, as it is after a start, not as a start that
// failed.
func TestAWorkerKilledForMemoryBeforeItStartedFailsTheSame(t *testing.T) {
	t.Parallel()

	report := newReporter()
	sup := &Supervisor{}
	sup.ended(t.Context(), &Worker{id: "dsp_1", dir: t.TempDir(), r: report, turn: &fakeTurn{}, pid: 42},
		shim.Exit{Code: -1, Signal: "killed", OOMKills: 1})

	assert.Equal(t, 1, report.startedCount(), "the worker ran: it is reported started first")
	require.Len(t, report.finished, 1)
	f := report.finished[0]
	assert.Equal(t, "worker_error", f.Class)
	assert.Equal(t, nodeproto.Exit{Kind: "signaled", Signal: "killed", Reason: "oom_killed"}, f.Exit)
	assert.True(t, strings.HasPrefix(f.Summary, "oom_killed: the kernel killed a process of this worker"), f.Summary)
}

// Scenario: the node updated itself while a worker ran, and the new daemon adopts it (issue #1313 row 1, review round
// 1). The worker, which an earlier daemon started in its own cgroup, is moved into its Dispatch's scope inside the
// workers' slice, with its shim and the CLI under it, so no worker runs outside the ceiling. A stand-in for busctl
// records the transient scope it is asked for; with HYPOMNEMA_TEST_SYSTEMD_SCOPE set, the user's real systemd does
// it and the processes' cgroups are read.
func TestAnAdoptedWorkerIsMovedIntoItsScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd is Linux's")
	}

	g := newRig(t, "hang")
	first := newReporter()
	g.start(first)

	again := g.restart()

	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	limit := memlimit.Limit{SystemdRun: "systemd-run", Systemctl: "systemctl", Busctl: filepath.Join(bin, "busctl")}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + args + "\n"
	require.NoError(t, os.WriteFile(limit.Busctl, []byte(script), 0o700)) //nolint:gosec // a private stand-in

	onSystemd := os.Getenv("HYPOMNEMA_TEST_SYSTEMD_SCOPE") != ""
	if onSystemd {
		limit = memlimit.Probe(t.Context())
		require.True(t, limit.On(), limit.Reason)
	}

	again.sup.Memory = limit
	second := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, again.dispatch(), second))

	pid, err := shim.PID(again.sup.dir("dsp_1"))
	require.NoError(t, err)

	tree := shim.Tree(pid)
	require.GreaterOrEqual(t, len(tree), 2, "the shim and the fake CLI under it")

	if onSystemd {
		for _, p := range tree {
			require.Eventually(t, func() bool {
				raw, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p), "cgroup"))
				return strings.Contains(string(raw), "/hypomnema-workers.slice/hypomnema-worker-dsp_1.scope")
			}, 5*time.Second, 50*time.Millisecond, "process %d is in the worker's scope", p)
		}
	} else {
		require.Eventually(t, func() bool { _, err := os.Stat(args); return err == nil }, 5*time.Second,
			20*time.Millisecond)
		raw, err := os.ReadFile(args) //nolint:gosec // the stand-in's own file
		require.NoError(t, err)
		got := strings.Split(strings.TrimSpace(string(raw)), "\n")
		assert.Equal(t, []string{
			"--user", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager",
			"StartTransientUnit", "ssa(sv)a(sa(sv))", "hypomnema-worker-dsp_1.scope", "fail",
		}, got[:9])
		joined := " " + strings.Join(got, " ") + " "
		assert.Contains(t, joined, " Slice s hypomnema-workers.slice ")
		// The worker's processes and the holder that keeps its scope until its end is read.
		assert.Contains(t, joined, " PIDs au "+strconv.Itoa(len(tree)+1)+" ")
		for _, p := range tree {
			assert.Contains(t, joined, " "+strconv.Itoa(p)+" ")
		}
	}

	again.sup.Stop("dsp_1")
	second.wait(t)
	assert.Equal(t, 1, second.stopped)
}

// Scenario: on a node that limits its workers' memory, the shim is started through systemd-run in the Dispatch's
// own scope inside the workers' slice, with the profile's limits.memory as the scope's MemoryMax, and the shim is
// told it runs in a scope (issue #1313 rows 1, 5). A stand-in for systemd-run records its arguments and runs the
// shim.
func TestAWorkerRunsInItsScopeInTheWorkersSlice(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("systemd is Linux's")
	}

	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	systemdRun := filepath.Join(bin, "systemd-run")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + args +
		"\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n"
	require.NoError(t, os.WriteFile(systemdRun, []byte(script), 0o700)) //nolint:gosec // a private stand-in

	g := newRig(t, "hang")
	g.sup.Memory = memlimit.Limit{SystemdRun: systemdRun, Systemctl: "systemctl", OOMPolicy: true}

	d := g.dispatch()
	d.Offer.MemoryLimit = 4 << 30

	report := newReporter()
	require.NoError(t, g.sup.Start(t.Context(), g.rt, d, report))
	require.Eventually(t, func() bool { return report.startedCount() == 1 }, 20*time.Second, 20*time.Millisecond)

	raw, err := os.ReadFile(args) //nolint:gosec // the stand-in's own file
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--user", "--scope", "--quiet", "--collect", "--slice=hypomnema-workers.slice",
		"--unit=hypomnema-worker-dsp_1.scope", "--property=OOMPolicy=continue", "--property=MemoryMax=4294967296", "--",
		g.sup.Exe, "shim", "--dir", g.sup.dir("dsp_1"),
	}, strings.Split(strings.TrimSpace(string(raw)), "\n"))

	g.sup.Stop("dsp_1")
	report.wait(t)
	assert.Equal(t, 1, report.stopped)
}

// Scenario: on a real node whose user manager delegates memory, the workers' slice is given a ceiling of 512 MiB and
// a worker takes 2 GiB (issue #1313's acceptance). The kernel kills it inside the slice, this process, outside it as
// the daemon is, goes on, and the worker ends oom_killed. It sets hypomnema-workers.slice itself, and reverts it after,
// so it runs only when HYPOMNEMA_TEST_WORKER_SLICE is set, on a node whose own workers do not use that slice.
func TestALiveWorkerOverTheSliceEndsOOMKilled(t *testing.T) {
	if os.Getenv("HYPOMNEMA_TEST_WORKER_SLICE") == "" {
		t.Skip("set HYPOMNEMA_TEST_WORKER_SLICE=1 on a node with no workers in hypomnema-workers.slice")
	}

	limit := memlimit.Probe(t.Context())
	require.True(t, limit.On(), limit.Reason)

	ceiling := &memlimit.Ceiling{Limit: limit, Logger: slog.Default()}
	ceiling.Apply(t.Context(), 512<<20)
	t.Cleanup(func() {
		revert := exec.Command(limit.Systemctl, "--user", "revert", nodeproto.WorkerSlice) //nolint:noctx,gosec // cleanup
		out, err := revert.CombinedOutput()
		t.Logf("reverted %s: %s %v", nodeproto.WorkerSlice, out, err)
	})

	props := []string{
		"--user", "show", nodeproto.WorkerSlice, "-p", "MemoryMax", "-p", "MemoryHigh", "-p", "MemorySwapMax",
	}
	show := exec.CommandContext(t.Context(), limit.Systemctl, props...) //nolint:gosec // systemd's own tool
	out, err := show.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Logf("%s:\n%s", nodeproto.WorkerSlice, out)

	g := newRig(t, "hog")
	g.sup.Memory = limit

	report := newReporter()
	started := time.Now()
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch(), report))

	cgroup, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(g.sup.get("dsp_1").shimPID()), "cgroup"))
	require.NoError(t, err)
	t.Logf("the shim's cgroup: %s", cgroup)
	assert.Contains(t, string(cgroup), "/hypomnema-workers.slice/hypomnema-worker-dsp_1.scope")

	select {
	case <-report.done:
	case <-time.After(15 * time.Minute):
		t.Fatal("the worker was not killed")
	}

	require.Len(t, report.finished, 1)
	f := report.finished[0]
	t.Logf("after %s: %+v", time.Since(started).Round(time.Second), f)
	assert.Equal(t, "worker_error", f.Class)
	assert.Equal(t, nodeproto.ExitOOMKilled, f.Exit.Reason)
	assert.True(t, strings.HasPrefix(f.Summary, "oom_killed: "), f.Summary)
}

// Scenario (review round 2): an adopted worker whose earlier daemon died before its started report went is killed for
// memory. Its control plane may still have it starting, which takes no worker_error: the node says it started again
// first (a control plane that knows refuses it), then that it failed for memory.
func TestAnAdoptedWorkerKilledForMemoryIsSaidStartedFirst(t *testing.T) {
	t.Parallel()

	report := newReporter()
	sup := &Supervisor{}
	sup.ended(t.Context(), &Worker{
		id: "dsp_1", dir: t.TempDir(), r: report, turn: &fakeTurn{}, pid: 42, started: true, adopted: true,
	}, shim.Exit{Code: -1, Signal: "killed", OOMKills: 1})

	assert.Equal(t, 1, report.startedCount(), "the started report goes again")
	require.Len(t, report.finished, 1)
	assert.Equal(t, "worker_error", report.finished[0].Class)
	assert.Equal(t, "oom_killed", report.finished[0].Exit.Reason)
}

// Scenario (review round 2, live): a worker an earlier daemon started outside the slice, with a shim that does not
// count kills for memory, is adopted into a scope of 64 MiB; it finishes, then a process of its is killed for memory
// and it ends at once, its scope emptied before the Supervisor's next read. The holder keeps the scope until its end
// is read, so the Dispatch fails oom_killed rather than succeeding. It starts an OOM kill in a real scope, so it runs
// only when HYPOMNEMA_TEST_WORKER_SLICE is set, on a node with no real services.
func TestAnAdoptedWorkersKillForMemoryIsNotLost(t *testing.T) {
	if os.Getenv("HYPOMNEMA_TEST_WORKER_SLICE") == "" {
		t.Skip("set HYPOMNEMA_TEST_WORKER_SLICE=1 on a node with no real services")
	}

	g := newRig(t, "hang")
	g.start(newReporter())

	again := g.restart()
	again.sup.Memory = memlimit.Probe(t.Context())
	require.True(t, again.sup.Memory.On(), again.sup.Memory.Reason)

	d := again.dispatch()
	d.Offer.MemoryLimit = 64 << 20

	report := newReporter()
	require.NoError(t, again.sup.Adopt(t.Context(), again.rt, d, report))

	pid, err := shim.PID(again.sup.dir("dsp_1"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
		return strings.Contains(string(raw), "/hypomnema-worker-dsp_1.scope")
	}, 10*time.Second, 20*time.Millisecond)

	w := again.sup.get("dsp_1")
	require.NotNil(t, w)
	require.NoError(t, w.Send(t.Context(), []byte(`{"type":"finish-hog"}`)))

	report.wait(t)
	require.Len(t, report.finished, 1)
	f := report.finished[0]
	t.Logf("%+v", f)
	assert.Equal(t, "worker_error", f.Class)
	assert.Equal(t, "oom_killed", f.Exit.Reason)
	assert.True(t, strings.HasPrefix(f.Summary, "oom_killed: "), f.Summary)
	assert.NoFileExists(t, filepath.Join(again.sup.dir("dsp_1"), "scope-holder.json"), "the holder is stopped")
}
