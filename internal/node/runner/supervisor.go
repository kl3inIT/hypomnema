// Package runner holds what the node's workers share, whatever their CLI
// (issue #392): the Supervisor, which keeps each worker's lifecycle, and the
// worker's first prompt. Each worker runs under a shim (internal/node/shim)
// in its own session, so it survives a daemon restart. The Supervisor reads
// the shim's journal from a saved offset and hands each line to the turn of
// the worker's Runtime, which knows its CLI. The worker talks back through
// two MCP tools (internal/node/workertools) over the bridge directory: ask,
// whose questions and answers the Supervisor relays, and finish, whose report
// is the outcome. A worker that exits without finish fails, as Orca reports a
// stop without worker_done.
//
// One lifecycle for every CLI follows Omnara's one map of processes behind
// its processRunner and Orca's session host, whose dialects supply the
// launch and keep "lifecycle and row identity shared"; one stop for every CLI
// follows Omnigent's uniform interrupt.
package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Timers.
const (
	// progressEvery bounds how often progress is reported (inferred).
	progressEvery = 30 * time.Second
	// pollEvery is how often the journal is read when it has nothing new.
	pollEvery = 200 * time.Millisecond
	// stopGrace: a worker asked to stop that is still alive after this is killed.
	stopGrace = 30 * time.Second
	// socketWait bounds the wait for a new shim's input socket.
	socketWait = 10 * time.Second
	offsetFile = "offset"
)

// Runtime is what one agent CLI supplies; Supervisor does the rest.
type Runtime interface {
	// Prepare refuses an offer this node cannot run and readies what lives
	// outside the run directory (CLI home, skills).
	Prepare(ctx context.Context, d statedb.Dispatch) (Launch, error)
	// Adopted is the state of a worker that an earlier daemon started.
	Adopted() Turn
}

// Launch is how one worker is started.
type Launch struct {
	// Format names the journal's format (transcript.SetFormat).
	Format string
	// Write writes the CLI's files into the run directory and says what the
	// shim runs; hostEnv is the git host's environment for the worktree.
	Write func(dir string, hostEnv []string) (shim.Spec, error)
	// First is sent once the shim's socket answers.
	First [][]byte
	Turn  Turn
}

// Turn is the state of one worker's turn, as its CLI tells it.
type Turn interface {
	// Line handles one journal line. It is called from one goroutine at a time.
	Line(ctx context.Context, w *Worker, line []byte)
	// Interrupt is the line that asks the CLI to stop; nil: it is killed at once.
	Interrupt() []byte
	// Result is how the turn went, read once the worker exited.
	Result() Result
	// Outcome is a turn that ended without finish.
	Outcome(ex nodeproto.Exit) nodeproto.Finished
}

// Result is what the CLI said of its turn.
type Result struct {
	// Done says the CLI ended the turn with its result.
	Done bool
	// Failure is what the CLI said went wrong.
	Failure string
	Usage   *nodeproto.Usage
}

// Session is the CLI's session a Dispatch continues: the one its acceptance
// names, else its offer's.
func Session(d statedb.Dispatch) string {
	if d.Accepted != nil && d.Accepted.CLISessionRef != "" {
		return d.Accepted.CLISessionRef
	}

	return d.Offer.CLISessionRef
}

// Supervisor runs the node's workers, of every runtime.
type Supervisor struct {
	// Home is the node's home: run/<dispatch> holds each shim.
	Home string
	// Exe is the node binary, run as `Exe shim --dir D`.
	Exe string
	// URL is the control plane's base URL, where a worker's tools reach the agent API.
	URL       string
	Worktrees worktree.Maker
	Logger    *slog.Logger

	mu   sync.Mutex
	jobs map[string]*Worker
}

// Worker is one worker the Supervisor follows.
type Worker struct {
	id     string
	dir    string
	pid    int
	r      daemon.Reporter
	turn   Turn
	logger *slog.Logger
	cancel context.CancelFunc

	mu       sync.Mutex
	started  bool
	stopping bool
	progress time.Time
}

// ID is the worker's Dispatch.
func (w *Worker) ID() string { return w.id }

// Dir is the worker's run directory.
func (w *Worker) Dir() string { return w.dir }

// Logger is the node's logger.
func (w *Worker) Logger() *slog.Logger { return w.logger }

// Started reports the worker started with the CLI's session, once.
func (w *Worker) Started(ctx context.Context, session string) {
	w.mu.Lock()
	first := !w.started
	w.started = true
	w.mu.Unlock()

	if first {
		w.Report(ctx, w.r.Started(ctx, "shim:"+strconv.Itoa(w.pid), session))
	}
}

// Progress reports progress, at most every progressEvery.
func (w *Worker) Progress(ctx context.Context) {
	w.mu.Lock()
	due := time.Since(w.progress) >= progressEvery
	if due {
		w.progress = time.Now()
	}
	w.mu.Unlock()

	if due {
		w.Report(ctx, w.r.Progress(ctx))
	}
}

// Send writes lines to the worker's input.
func (w *Worker) Send(ctx context.Context, lines ...[]byte) error {
	return shim.Send(ctx, w.dir, lines...)
}

// Report logs an error of reporting or of writing to the worker.
func (w *Worker) Report(ctx context.Context, err error) {
	if err != nil {
		w.logger.WarnContext(ctx, "reporting a worker",
			slog.String("event", "node.report_failed"), slog.String("dispatch_id", w.id), slog.String("error", err.Error()))
	}
}

func (s *Supervisor) dir(id string) string { return filepath.Join(s.Home, "run", id) }

func (s *Supervisor) put(w *Worker) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.jobs == nil {
		s.jobs = map[string]*Worker{}
	}

	if _, ok := s.jobs[w.id]; ok {
		return false
	}

	s.jobs[w.id] = w

	return true
}

func (s *Supervisor) get(id string) *Worker {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.jobs[id]
}

func (s *Supervisor) drop(id string) *Worker {
	s.mu.Lock()
	defer s.mu.Unlock()

	w := s.jobs[id]
	delete(s.jobs, id)

	return w
}

// Start prepares the runtime and the worktree, starts the shim, and sends
// the first lines.
func (s *Supervisor) Start(ctx context.Context, rt Runtime, d statedb.Dispatch, r daemon.Reporter) error {
	o := d.Offer

	launch, err := rt.Prepare(ctx, d)
	if err != nil {
		return err
	}

	var hostEnv []string

	if o.Repo != "" {
		src, err := worktree.SourceOf(o.RepoHost, o.RepoKind, o.Repo, o.RepoToken)
		if err != nil {
			return err
		}

		// The worker's git, gh and glab act with the host's token, when the offer carried one.
		hostEnv = src.Env

		if err := s.Worktrees.Ensure(ctx, src, o.Base, o.TaskID, o.Worktree); err != nil {
			return err
		}
	} else if err := os.MkdirAll(o.Worktree, 0o700); err != nil {
		return fmt.Errorf("making the worktree: %w", err)
	}

	dir := s.dir(d.ID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	spec, err := launch.Write(dir, hostEnv)
	if err != nil {
		return err
	}

	spec.Dir = o.Worktree
	if err := shim.WriteSpec(dir, spec); err != nil {
		return err
	}

	// The worker's tools server reaches the shared browser with the Dispatch's key (issue #212).
	if s.URL != "" && o.AgentKey != "" {
		if err := bridge.SetAgent(bridge.Dir(dir), bridge.Agent{URL: s.URL, Key: o.AgentKey}); err != nil {
			return err
		}
	}

	if err := transcript.SetFormat(dir, launch.Format); err != nil {
		return err
	}

	pid, err := shim.Start(s.Exe, dir)
	if err != nil {
		return err
	}

	if err := waitSocket(ctx, dir); err != nil {
		_ = shim.Kill(pid)

		return err
	}

	if err := shim.Send(ctx, dir, launch.First...); err != nil {
		_ = shim.Kill(pid)

		return err
	}

	s.watch(ctx, &Worker{id: d.ID, dir: dir, pid: pid, r: r, turn: launch.Turn})

	return nil
}

func waitSocket(ctx context.Context, dir string) error {
	deadline := time.Now().Add(socketWait)

	for {
		// The socket file appears at bind, before the shim listens; only a connection proves it is ready.
		if conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, shim.SocketFile)); err == nil {
			_ = conn.Close()

			return nil
		}

		if _, done, _ := shim.ReadExit(dir); done {
			return fmt.Errorf("the worker exited at once: %s", strings.TrimSpace(stderr(dir)))
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("the shim did not open its input socket: %s", strings.TrimSpace(stderr(dir)))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Adopt follows a worker started by an earlier daemon, from where its journal
// was last read.
func (s *Supervisor) Adopt(ctx context.Context, rt Runtime, d statedb.Dispatch, r daemon.Reporter) error {
	dir := s.dir(d.ID)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("nothing of %s is left on the node: %w", d.ID, err)
	}

	pid, _ := shim.PID(dir)

	s.watch(ctx, &Worker{id: d.ID, dir: dir, pid: pid, r: r, turn: rt.Adopted(), started: true})

	return nil
}

// watch follows the worker until it ends; it outlives the call that started it.
func (s *Supervisor) watch(ctx context.Context, w *Worker) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel, w.logger = cancel, s.Logger

	if !s.put(w) {
		cancel()

		return
	}

	go s.tail(ctx, w)
}

// Stop sends the turn's interrupt and closes the worker's input; one still
// alive after stopGrace is killed. A turn with no interrupt is killed at once.
func (s *Supervisor) Stop(id string) {
	w := s.get(id)
	if w == nil {
		return
	}

	w.mu.Lock()
	already := w.stopping
	w.stopping = true
	w.mu.Unlock()

	if already {
		return
	}

	interrupt := w.turn.Interrupt()
	if interrupt == nil {
		if w.pid > 0 {
			_ = shim.Kill(w.pid)
		}

		return
	}

	_ = shim.Send(context.Background(), w.dir, interrupt, []byte(shim.CloseLine))

	time.AfterFunc(stopGrace, func() {
		if _, done, _ := shim.ReadExit(w.dir); !done && w.pid > 0 {
			_ = shim.Kill(w.pid)
		}
	})
}

// Release kills the worker and forgets it, reporting nothing. The shim is
// found by its PID file, so a worker no daemon follows is killed too.
func (s *Supervisor) Release(id string) {
	w := s.drop(id)
	dir := s.dir(id)

	if w != nil {
		w.cancel()
	}

	if pid, err := shim.PID(dir); err == nil {
		_ = shim.Kill(pid)
	}

	_ = os.RemoveAll(dir)
}

// Asked records the control plane's id for a question the worker asked.
func (s *Supervisor) Asked(id, reportID, questionID string) {
	dir := bridge.Dir(s.dir(id))

	qs, err := bridge.Questions(dir)
	if err != nil {
		return
	}

	for _, q := range qs {
		if r, _ := bridge.Marked(dir, q.Seq, "report"); r == reportID {
			_ = bridge.Mark(dir, q.Seq, "question", questionID)
		}
	}
}

// Answer passes an answer to the waiting ask and acknowledges it. An answer
// for a question not yet known here is ignored; it comes again until
// acknowledged.
func (s *Supervisor) Answer(id string, q nodeproto.Question) {
	w := s.get(id)
	if w == nil || q.Answer == nil {
		return
	}

	dir := bridge.Dir(w.dir)

	qs, err := bridge.Questions(dir)
	if err != nil {
		return
	}

	ctx := context.Background()

	for _, mine := range qs {
		if qid, _ := bridge.Marked(dir, mine.Seq, "question"); qid != q.ID {
			continue
		}

		if _, done, _ := bridge.Answered(dir, mine.Seq); !done {
			if err := bridge.Answer(dir, mine.Seq, answerText(q)); err != nil {
				return
			}
		}

		w.Report(ctx, w.r.AnswerReceived(ctx, q.ID))

		return
	}
}

func answerText(q nodeproto.Question) string {
	a := q.Answer
	label := ""

	if a.Option != nil && *a.Option >= 0 && *a.Option < len(q.Options) {
		label = q.Options[*a.Option].Label
	}

	return strings.TrimSpace(label + " " + a.Text)
}

// relay submits the questions the worker asked that the daemon has not.
func relay(ctx context.Context, w *Worker) {
	dir := bridge.Dir(w.dir)

	qs, err := bridge.Questions(dir)
	if err != nil {
		return
	}

	for _, q := range qs {
		if _, sent := bridge.Marked(dir, q.Seq, "report"); sent {
			continue
		}

		reportID, err := w.r.Ask(ctx, q.Ask)
		if err != nil {
			w.Report(ctx, err)

			continue
		}

		_ = bridge.Mark(dir, q.Seq, "report", reportID)
	}
}

// tail reads the journal from the saved offset until the worker exits.
func (s *Supervisor) tail(ctx context.Context, w *Worker) {
	offset := readOffset(w.dir)

	for {
		relay(ctx, w)

		n, err := readFrom(ctx, w, offset)
		offset += n

		if n > 0 {
			writeOffset(w.dir, offset)
		}

		if err != nil {
			s.Logger.WarnContext(ctx, "reading a worker's journal",
				slog.String("event", "node.journal_failed"), slog.String("dispatch_id", w.id),
				slog.String("error", err.Error()))
		}

		if n == 0 {
			exit, done, _ := shim.ReadExit(w.dir)
			if done {
				// Read once more: the last lines may have landed with the exit.
				if more, _ := readFrom(ctx, w, offset); more > 0 {
					offset += more
					writeOffset(w.dir, offset)

					continue
				}

				// A question asked just before the exit is still relayed.
				relay(ctx, w)
				s.ended(ctx, w, exit)

				return
			}

			if w.pid > 0 && !shim.Alive(w.pid) {
				s.ended(ctx, w, shim.Exit{Code: -1, Error: "the shim died"})

				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(pollEvery):
		}
	}
}

// readFrom hands every complete line after offset to the turn and returns
// how many bytes it consumed.
func readFrom(ctx context.Context, w *Worker, offset int64) (int64, error) {
	f, err := os.Open(filepath.Join(w.dir, shim.JournalFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck // read only

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}

	br := bufio.NewReaderSize(f, 64<<10)

	var n int64

	for {
		line, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			// A partial line waits for the rest.
			return n, nil
		}

		if err != nil {
			return n, err
		}

		n += int64(len(line))

		w.turn.Line(ctx, w, line)
	}
}

// ended reports the outcome once the worker exited.
func (s *Supervisor) ended(ctx context.Context, w *Worker, exit shim.Exit) {
	s.drop(w.id)

	w.mu.Lock()
	stopping, started := w.stopping, w.started
	w.mu.Unlock()

	res := w.turn.Result()

	code := exit.Code
	ex := nodeproto.Exit{Kind: "exited", Code: &code, Signal: exit.Signal}

	if exit.Signal != "" {
		ex = nodeproto.Exit{Kind: "signaled", Signal: exit.Signal}
	}

	switch {
	case stopping:
		w.Report(ctx, w.r.Stopped(ctx))

		return
	case !started && !res.Done:
		w.Report(ctx, w.r.Finished(ctx, nodeproto.Finished{
			Class: "start_failed", Summary: FirstLine(res.Failure, exit.Error, stderr(w.dir)), Exit: ex,
		}))

		return
	}

	final, finished, err := bridge.Finished(bridge.Dir(w.dir))
	if err != nil {
		w.Report(ctx, err)
	}

	switch {
	case finished:
		w.Report(ctx, w.r.Finished(ctx, nodeproto.Finished{
			Summary: final.Summary, Evidence: final.Evidence, Gate: final.Gate, Exit: ex, Usage: res.Usage,
		}))
	case !res.Done && res.Failure == "":
		// No result and nothing said: the outcome is unknown.
		w.Report(ctx, w.r.Exited(ctx))
	default:
		w.Report(ctx, w.r.Finished(ctx, w.turn.Outcome(ex)))
	}
}

func stderr(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, shim.StderrFile)) //nolint:gosec // the runner's own directory

	return string(raw)
}

// FirstLine is the first line of the first value that has one.
func FirstLine(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			line, _, _ := strings.Cut(v, "\n")

			return line
		}
	}

	return "the worker did not start"
}

func readOffset(dir string) int64 {
	raw, err := os.ReadFile(filepath.Join(dir, offsetFile)) //nolint:gosec // the runner's own directory
	if err != nil {
		return 0
	}

	n, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)

	return n
}

func writeOffset(dir string, n int64) {
	tmp := filepath.Join(dir, offsetFile+".tmp")
	if os.WriteFile(tmp, []byte(strconv.FormatInt(n, 10)), 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, offsetFile))
	}
}
