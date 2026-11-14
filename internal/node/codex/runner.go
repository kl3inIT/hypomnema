// Package codex runs Codex CLI workers for the node daemon (issue #107). A
// worker's model is served by the control plane's model gateway, under an
// account logged in there: no ChatGPT login exists on the node.
//
// Each worker runs under a shim in its own session, as a Claude Code worker
// does (internal/node/claude), and talks back through the same two tools, ask
// and finish, over the bridge directory. What differs is the CLI:
//
//	codex exec --json --skip-git-repo-check --ignore-user-config
//	  --dangerously-bypass-approvals-and-sandbox
//	  -c model_provider=… -c model_providers.<id>.… -c mcp_servers.hypomnema.…
//	  [resume <thread id>] -
//
// with the prompt on standard input. It follows the references that point
// Codex at a gateway (docs/research/2026-11-10-codex-responses-passthrough.md):
// the provider is named with -c overrides and the key travels in an
// environment variable, never a file (durable-harness, Coder's docs,
// 9router-go's clientconfig/codex.go); CODEX_HOME is the Conversation's own,
// so `exec resume` finds the session (durable-harness, hermes-agent); exec
// never asks for approval, and the sandbox is off as Claude Code's runner
// allows every tool. One process is one turn: it ends by itself.
//
// The worker's transcript is read from the same journal (items.go).
package codex

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/skills"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Timers, as the Claude Code runner's.
const (
	progressEvery = 30 * time.Second
	pollEvery     = 200 * time.Millisecond
	socketWait    = 10 * time.Second
	offsetFile    = "offset"
	// toolTimeout lets an ask wait a day for its answer.
	toolTimeout = 24 * time.Hour
)

const (
	// providerID names the gateway in the CLI's configuration.
	providerID = "hypomnema"
	// keyVariable is the environment variable the CLI reads the gateway's key from.
	keyVariable = "HYPOMNEMA_MODEL_KEY"
)

// Runner runs Codex CLI workers.
type Runner struct {
	// Home is the node's home: run/<dispatch> holds each shim,
	// conversations/<conversation>/codex each Conversation's CODEX_HOME.
	Home string
	// Exe is the node binary, run as `Exe shim --dir D` and `Exe mcp --dir D`.
	Exe string
	// Codex is the codex binary.
	Codex string
	// URL is the control plane's base URL; the gateway is at URL/v1.
	URL       string
	Worktrees worktree.Maker
	// Env is added to each worker's environment (tests).
	Env    []string
	Logger *slog.Logger

	mu   sync.Mutex
	jobs map[string]*job
}

type job struct {
	id     string
	dir    string
	r      daemon.Reporter
	cancel context.CancelFunc

	mu       sync.Mutex
	pid      int
	started  bool
	stopping bool
	// done says a turn completed; failure is what the CLI said when one did not.
	done     bool
	failure  string
	usage    *nodeproto.Usage
	progress time.Time
}

func (rn *Runner) dir(id string) string { return filepath.Join(rn.Home, "run", id) }

func (rn *Runner) put(j *job) bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	if rn.jobs == nil {
		rn.jobs = map[string]*job{}
	}

	if _, ok := rn.jobs[j.id]; ok {
		return false
	}

	rn.jobs[j.id] = j

	return true
}

func (rn *Runner) get(id string) *job {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	return rn.jobs[id]
}

func (rn *Runner) drop(id string) *job {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	j := rn.jobs[id]
	delete(rn.jobs, id)

	return j
}

// quote writes a string as a TOML value for a -c override.
func quote(s string) string { return strconv.Quote(s) }

// argv is the command of one turn.
func (rn *Runner) argv(o nodeproto.Offer, dir, session string) []string {
	provider := "model_providers." + providerID
	tools := "mcp_servers.hypomnema"

	args, _ := json.Marshal([]string{"mcp", "--dir", bridge.Dir(dir)}) //nolint:errchkjson // Strings always encode.

	argv := []string{
		rn.Codex, "exec", "--json", "--skip-git-repo-check", "--ignore-user-config",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c", "model_provider=" + quote(providerID),
		"-c", provider + ".name=" + quote("Hypomnema"),
		"-c", provider + ".base_url=" + quote(strings.TrimRight(rn.URL, "/")+"/v1"),
		"-c", provider + ".env_key=" + quote(keyVariable),
		"-c", provider + ".wire_api=" + quote("responses"),
		"-c", provider + ".supports_websockets=false",
		// The gateway answers Codex's standalone search, its web.run tool, at <base URL>/alpha/search (issue #354);
		// Codex offers the tool only to a provider that says so (codex-rs ext/web-search/src/extension.rs), as
		// OpenClaw sets it for a provider that serves the endpoint (docs/plugins/codex-harness/runtime-behavior.md).
		"-c", provider + ".supports_standalone_web_search=true",
		"-c", tools + ".command=" + quote(rn.Exe),
		"-c", tools + ".args=" + string(args),
		"-c", tools + ".tool_timeout_sec=" + strconv.Itoa(int(toolTimeout.Seconds())),
	}

	if o.Model != "" {
		argv = append(argv, "-c", "model="+quote(o.Model))
	}

	if o.Effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+quote(effort(o.Effort)))
	}

	if session != "" {
		argv = append(argv, "resume", session)
	}

	// The prompt is read from standard input.
	return append(argv, "-")
}

// effort is a profile's effort as Codex names it: it has none above xhigh.
func effort(e string) string {
	if e == "max" {
		return "xhigh"
	}

	return e
}

// Start prepares the worktree and the Conversation's CODEX_HOME, starts the
// shim, and sends the prompt.
func (rn *Runner) Start(ctx context.Context, d statedb.Dispatch, r daemon.Reporter) error {
	o := d.Offer

	switch {
	case rn.Codex == "":
		return errors.New("codex is not on this node's PATH")
	case o.ModelKey == "":
		return errors.New("the offer carries no key of the model gateway: " +
			"a Codex worker runs under a profile whose provider is the gateway")
	case rn.URL == "":
		return errors.New("the node does not know its control plane's URL")
	}

	var hostEnv []string

	if o.Repo != "" {
		src, err := worktree.SourceOf(o.RepoHost, o.RepoKind, o.Repo, o.RepoToken)
		if err != nil {
			return err
		}

		// The worker's git, gh and glab act with the host's token, when the offer carried one.
		hostEnv = src.Env

		if err := rn.Worktrees.Ensure(ctx, src, o.Base, o.TaskID, o.Worktree); err != nil {
			return err
		}
	} else if err := os.MkdirAll(o.Worktree, 0o700); err != nil {
		return fmt.Errorf("making the worktree: %w", err)
	}

	codexHome := filepath.Join(rn.Home, "conversations", o.ConversationID, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return fmt.Errorf("making the conversation's CODEX_HOME: %w", err)
	}

	// The skills the worker is given, placed again at each start so that one taken away is gone (issue #21).
	if err := skills.Place(rn.Home, filepath.Join(codexHome, "skills"), o.Skills); err != nil {
		return err
	}

	session := o.CLISessionRef
	if d.Accepted != nil && d.Accepted.CLISessionRef != "" {
		session = d.Accepted.CLISessionRef
	}

	dir := rn.dir(d.ID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}

	toolEnv, err := agenttool.ForDispatch(rn.Home, rn.Exe, rn.URL, o.Uses, o.AgentKey)
	if err != nil {
		return err
	}

	spec := shim.Spec{
		Argv: rn.argv(o, dir, session), Dir: o.Worktree,
		Env: append(append(rn.env(codexHome, o.ModelKey), hostEnv...), toolEnv...),
	}
	if err := shim.WriteSpec(dir, spec); err != nil {
		return err
	}

	// The worker's tools server reaches the shared browser with the Dispatch's key (issue #212).
	if rn.URL != "" && o.AgentKey != "" {
		if err := bridge.SetAgent(bridge.Dir(dir), bridge.Agent{URL: rn.URL, Key: o.AgentKey}); err != nil {
			return err
		}
	}

	if err := transcript.SetFormat(dir, Format); err != nil {
		return err
	}

	pid, err := shim.Start(rn.Exe, dir)
	if err != nil {
		return err
	}

	if err := waitSocket(ctx, dir); err != nil {
		_ = shim.Kill(pid)

		return err
	}

	// The whole prompt, then the end of input: exec reads it to the end.
	if err := shim.Send(ctx, dir, []byte(runner.Prompt(d)), []byte(shim.CloseLine)); err != nil {
		_ = shim.Kill(pid)

		return err
	}

	return rn.watch(ctx, d.ID, dir, pid, false, r)
}

// env is the worker's whole environment: a clean one, with the gateway's key
// and the Conversation's CODEX_HOME.
func (rn *Runner) env(codexHome, key string) []string {
	out := []string{"CODEX_HOME=" + codexHome, keyVariable + "=" + key, "LANG=C.UTF-8"}

	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SystemRoot", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}

	return append(out, rn.Env...)
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
			raw, _ := os.ReadFile(filepath.Join(dir, shim.StderrFile)) //nolint:gosec // the runner's own directory

			return fmt.Errorf("the worker exited at once: %s", strings.TrimSpace(string(raw)))
		}

		if time.Now().After(deadline) {
			return errors.New("the shim did not open its input socket")
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
func (rn *Runner) Adopt(ctx context.Context, d statedb.Dispatch, r daemon.Reporter) error {
	dir := rn.dir(d.ID)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("nothing of %s is left on the node: %w", d.ID, err)
	}

	pid, _ := shim.PID(dir)

	return rn.watch(ctx, d.ID, dir, pid, true, r)
}

// watch follows the worker until it ends; it outlives the call that started it.
func (rn *Runner) watch(ctx context.Context, id, dir string, pid int, started bool, r daemon.Reporter) error {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j := &job{id: id, dir: dir, r: r, cancel: cancel, pid: pid, started: started}

	if !rn.put(j) {
		cancel()

		return nil
	}

	go rn.tail(ctx, j)

	return nil
}

// Stop ends the worker: exec has no way to be interrupted, so its process is killed.
func (rn *Runner) Stop(id string) {
	j := rn.get(id)
	if j == nil {
		return
	}

	j.mu.Lock()
	already := j.stopping
	j.stopping = true
	j.mu.Unlock()

	if !already && j.pid > 0 {
		_ = shim.Kill(j.pid)
	}
}

// Release kills the worker and forgets it, reporting nothing.
func (rn *Runner) Release(id string) {
	j := rn.drop(id)
	if j == nil {
		return // Not a Codex worker's, or gone already.
	}

	j.cancel()

	if pid, err := shim.PID(j.dir); err == nil {
		_ = shim.Kill(pid)
	}

	_ = os.RemoveAll(j.dir)
}

// Asked records the control plane's id for a question the worker asked.
func (rn *Runner) Asked(id, reportID, questionID string) {
	j := rn.get(id)
	if j == nil {
		return
	}

	dir := bridge.Dir(j.dir)

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

// Answer passes an answer to the waiting ask and acknowledges it.
func (rn *Runner) Answer(id string, q nodeproto.Question) {
	j := rn.get(id)
	if j == nil || q.Answer == nil {
		return
	}

	dir := bridge.Dir(j.dir)

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

		rn.report(ctx, j, j.r.AnswerReceived(ctx, q.ID))

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
func (rn *Runner) relay(ctx context.Context, j *job) {
	dir := bridge.Dir(j.dir)

	qs, err := bridge.Questions(dir)
	if err != nil {
		return
	}

	for _, q := range qs {
		if _, sent := bridge.Marked(dir, q.Seq, "report"); sent {
			continue
		}

		reportID, err := j.r.Ask(ctx, q.Ask)
		if err != nil {
			rn.report(ctx, j, err)

			continue
		}

		_ = bridge.Mark(dir, q.Seq, "report", reportID)
	}
}

// event is the part of a line of `codex exec --json` the runner reads
// (codex-rs/exec/src/exec_events.rs).
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
}

// tail reads the journal from the saved offset until the worker exits.
func (rn *Runner) tail(ctx context.Context, j *job) {
	offset := readOffset(j.dir)

	for {
		rn.relay(ctx, j)

		n, err := rn.readFrom(ctx, j, offset)
		offset += n

		if n > 0 {
			writeOffset(j.dir, offset)
		}

		if err != nil {
			rn.Logger.WarnContext(ctx, "reading a worker's journal",
				slog.String("event", "node.journal_failed"), slog.String("dispatch_id", j.id),
				slog.String("error", err.Error()))
		}

		if n == 0 {
			exit, done, _ := shim.ReadExit(j.dir)
			if done {
				// Read once more: the last lines may have landed with the exit.
				if more, _ := rn.readFrom(ctx, j, offset); more > 0 {
					offset += more
					writeOffset(j.dir, offset)

					continue
				}

				rn.relay(ctx, j)
				rn.ended(ctx, j, exit)

				return
			}

			if j.pid > 0 && !shim.Alive(j.pid) {
				rn.ended(ctx, j, shim.Exit{Code: -1, Error: "the shim died"})

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

// readFrom handles every complete line after offset and returns how many
// bytes it consumed.
func (rn *Runner) readFrom(ctx context.Context, j *job, offset int64) (int64, error) {
	f, err := os.Open(filepath.Join(j.dir, shim.JournalFile))
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

		rn.handle(ctx, j, line)
	}
}

func (rn *Runner) handle(ctx context.Context, j *job, line []byte) {
	var e event
	if json.Unmarshal(line, &e) != nil {
		return
	}

	switch e.Type {
	case "thread.started":
		j.mu.Lock()
		first := !j.started
		j.started = true
		j.mu.Unlock()

		if first {
			rn.report(ctx, j, j.r.Started(ctx, "shim:"+strconv.Itoa(j.pid), e.ThreadID))
		}
	case "turn.completed":
		j.mu.Lock()
		// An error before this was one the CLI got over: it prints "Reconnecting…" as an error event.
		j.done, j.failure = true, ""

		if e.Usage != nil {
			// Codex counts the cached part inside the input.
			j.usage = &nodeproto.Usage{
				Input: max(e.Usage.Input-e.Usage.Cached, 0), Output: e.Usage.Output, CacheRead: e.Usage.Cached,
			}
		}
		j.mu.Unlock()
	case "turn.failed", "error":
		message := e.Message
		if e.Error != nil && e.Error.Message != "" {
			message = e.Error.Message
		}

		j.mu.Lock()
		j.failure = message
		j.mu.Unlock()
	default:
		j.mu.Lock()
		due := time.Since(j.progress) >= progressEvery
		if due {
			j.progress = time.Now()
		}
		j.mu.Unlock()

		if due {
			rn.report(ctx, j, j.r.Progress(ctx))
		}
	}
}

// ended reports the outcome once the worker exited.
func (rn *Runner) ended(ctx context.Context, j *job, exit shim.Exit) {
	rn.drop(j.id)

	j.mu.Lock()
	stopping, started, done, failure, used := j.stopping, j.started, j.done, j.failure, j.usage
	j.mu.Unlock()

	code := exit.Code
	ex := nodeproto.Exit{Kind: "exited", Code: &code, Signal: exit.Signal}

	if exit.Signal != "" {
		ex = nodeproto.Exit{Kind: "signaled", Signal: exit.Signal}
	}

	switch {
	case stopping:
		rn.report(ctx, j, j.r.Stopped(ctx))

		return
	case !started:
		rn.report(ctx, j, j.r.Finished(ctx, nodeproto.Finished{
			Class: "start_failed", Summary: firstLine(failure, exit.Error, rn.stderr(j)), Exit: ex,
		}))

		return
	}

	final, finished, err := bridge.Finished(bridge.Dir(j.dir))
	if err != nil {
		rn.report(ctx, j, err)
	}

	switch {
	case finished:
		rn.report(ctx, j, j.r.Finished(ctx, nodeproto.Finished{
			Summary: final.Summary, Evidence: final.Evidence, Gate: final.Gate, Exit: ex, Usage: used,
		}))
	case !done && failure == "":
		rn.report(ctx, j, j.r.Exited(ctx))
	default:
		rn.report(ctx, j, j.r.Finished(ctx, outcome(failure, ex, used)))
	}
}

// outcome is a worker that exited without calling finish: the gateway's key
// or the account behind it refused, a limit, or else a failure.
func outcome(failure string, ex nodeproto.Exit, used *nodeproto.Usage) nodeproto.Finished {
	f := nodeproto.Finished{Summary: failure, Exit: ex, Usage: used}
	lower := strings.ToLower(failure)

	switch {
	case strings.Contains(lower, "usage limit") || strings.Contains(lower, "429") ||
		strings.Contains(lower, "rate limit"):
		f.Class = "rate_limited"
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid_api_key"):
		f.Class = "needs_auth"
	default:
		f.Class = "worker_error"
		f.Summary = strings.TrimSpace("stopped without finish: " + failure)
	}

	return f
}

func (rn *Runner) stderr(j *job) string {
	raw, _ := os.ReadFile(filepath.Join(j.dir, shim.StderrFile))

	return string(raw)
}

func firstLine(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			line, _, _ := strings.Cut(v, "\n")

			return line
		}
	}

	return "the worker did not start"
}

func (rn *Runner) report(ctx context.Context, j *job, err error) {
	if err != nil {
		rn.Logger.WarnContext(ctx, "reporting a worker",
			slog.String("event", "node.report_failed"), slog.String("dispatch_id", j.id), slog.String("error", err.Error()))
	}
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
