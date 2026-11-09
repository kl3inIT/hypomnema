// Package claude runs Claude Code workers for the node daemon (issue #32).
// Each worker runs under a shim (internal/node/shim) in its own session, so
// it survives a daemon restart. The runner drives it over bidirectional
// stream-JSON, the protocol of spike B
// (docs/research/2026-11-06-headless-cli-drivers.md):
//
//	claude -p --input-format stream-json --output-format stream-json --verbose
//	  --permission-mode manual --permission-prompt-tool stdio [--resume <session>] [--model <m>]
//
// It reads the shim's journal from a saved offset and reports the session id
// from system/init and progress. The worker talks back through two MCP tools
// (internal/node/workertools) over the bridge directory: ask, whose
// questions and answers the runner relays, and finish, whose report is the
// outcome. A worker that exits without finish fails, as Orca reports a stop
// without worker_done. Every permission prompt is allowed and logged: the
// node's machine or user is the boundary, as Orca, OpenHands and Coder run
// their workers by default.
package claude

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
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
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
	mcpFile    = "mcp.json"
	// toolTimeout is the hard limit on one tool call, so an ask can wait a day
	// (spike 2: the default is about 27.8 h; set explicitly).
	toolTimeout = 24 * time.Hour
)

// Account is how the node runs Claude under one account until the vault
// exists: the file holding its `claude setup-token` token.
type Account struct {
	TokenFile string `json:"token_file"`
}

// Runner runs Claude Code workers.
type Runner struct {
	// Home is the node's home: run/<dispatch> holds each shim, accounts/<id> each account's config dir.
	Home string
	// Exe is the node binary, run as `Exe shim --dir D`.
	Exe string
	// Claude is the claude binary.
	Claude    string
	Worktrees worktree.Maker
	Accounts  map[string]Account
	// Env is added to each worker's environment (tests).
	Env    []string
	Logger *slog.Logger

	mu   sync.Mutex
	jobs map[string]*job
}

var _ daemon.Runner = (*Runner)(nil)

type job struct {
	id     string
	dir    string
	r      daemon.Reporter
	cancel context.CancelFunc

	mu       sync.Mutex
	pid      int
	started  bool
	stopping bool
	result   *event
	limited  *time.Time
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

// Start prepares the worktree and the account, starts the shim, and sends
// the first turn.
func (rn *Runner) Start(ctx context.Context, d statedb.Dispatch, r daemon.Reporter) error {
	o := d.Offer

	if rn.Claude == "" {
		return errors.New("claude is not on this node's PATH")
	}

	acc, ok := rn.Accounts[o.AccountRef]
	if !ok {
		return fmt.Errorf("the node has no credentials for account %s", o.AccountRef)
	}

	token, err := os.ReadFile(acc.TokenFile)
	if err != nil {
		return fmt.Errorf("reading the token of account %s: %w", o.AccountRef, err)
	}

	if o.Repo != "" {
		if err := rn.Worktrees.Ensure(ctx, o.Repo, o.Base, o.TaskID, o.Worktree); err != nil {
			return err
		}
	} else if err := os.MkdirAll(o.Worktree, 0o700); err != nil {
		return fmt.Errorf("making the worktree: %w", err)
	}

	configDir := filepath.Join(rn.Home, "accounts", o.AccountRef, "claude")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("making the account's config dir: %w", err)
	}

	session := o.CLISessionRef
	if d.Accepted != nil && d.Accepted.CLISessionRef != "" {
		session = d.Accepted.CLISessionRef
	}

	dir := rn.dir(d.ID)
	argv := []string{rn.Claude, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "manual", "--permission-prompt-tool", "stdio",
		"--mcp-config", filepath.Join(dir, mcpFile), "--strict-mcp-config", "--disallowedTools", "AskUserQuestion"}
	if session != "" {
		argv = append(argv, "--resume", session)
	}

	if o.Model != "" {
		argv = append(argv, "--model", o.Model)
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}

	spec := shim.Spec{Argv: argv, Env: rn.env(configDir, string(token)), Dir: o.Worktree}
	if err := shim.WriteSpec(dir, spec); err != nil {
		return err
	}

	if err := rn.writeMCPConfig(dir); err != nil {
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

	first, err := json.Marshal(map[string]any{
		"type": "user", "message": map[string]any{"role": "user", "content": Prompt(d)},
	})
	if err != nil {
		return err
	}

	initialize := []byte(`{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}`)
	if err := shim.Send(ctx, dir, initialize, first); err != nil {
		_ = shim.Kill(pid)

		return err
	}

	return rn.watch(ctx, d.ID, dir, pid, false, r)
}

// env is the worker's whole environment: a clean one (durable-harness
// env.ts) with the account's token and its own config dir (Orca's
// CLAUDE_CONFIG_DIR per account).
func (rn *Runner) env(configDir, token string) []string {
	out := []string{"CLAUDE_CONFIG_DIR=" + configDir, "CLAUDE_CODE_OAUTH_TOKEN=" + strings.TrimSpace(token),
		"LANG=C.UTF-8", "MCP_TOOL_TIMEOUT=" + strconv.FormatInt(toolTimeout.Milliseconds(), 10)}

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

// Stop interrupts the worker and closes its input; one still alive after
// stopGrace is killed.
func (rn *Runner) Stop(id string) {
	j := rn.get(id)
	if j == nil {
		return
	}

	j.mu.Lock()
	already := j.stopping
	j.stopping = true
	j.mu.Unlock()

	if already {
		return
	}

	ctx := context.Background()
	_ = shim.Send(ctx, j.dir, []byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"interrupt"}}`),
		[]byte(shim.CloseLine))

	time.AfterFunc(stopGrace, func() {
		if _, done, _ := shim.ReadExit(j.dir); !done && j.pid > 0 {
			_ = shim.Kill(j.pid)
		}
	})
}

// Release kills the worker and forgets it, reporting nothing.
func (rn *Runner) Release(id string) {
	j := rn.drop(id)
	dir := rn.dir(id)

	if j != nil {
		j.cancel()
	}

	if pid, err := shim.PID(dir); err == nil {
		_ = shim.Kill(pid)
	}

	_ = os.RemoveAll(dir)
}

// writeMCPConfig names the worker's tools server: this binary's `mcp`
// command over the bridge directory.
func (rn *Runner) writeMCPConfig(dir string) error {
	raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"hypomnema": map[string]any{
			"type": "stdio", "command": rn.Exe, "args": []string{"mcp", "--dir", bridge.Dir(dir)},
			"timeout": toolTimeout.Milliseconds(),
		},
	}})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, mcpFile), raw, 0o600)
}

// Asked records the control plane's id for a question the worker asked.
func (rn *Runner) Asked(id, reportID, questionID string) {
	dir := bridge.Dir(rn.dir(id))

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

// event is the part of a stream-JSON line the runner reads.
type event struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	Usage     *struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	RateLimit *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
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

	switch {
	case e.Type == "system" && e.Subtype == "init":
		j.mu.Lock()
		first := !j.started
		j.started = true
		j.mu.Unlock()

		if first {
			rn.report(ctx, j, j.r.Started(ctx, "shim:"+strconv.Itoa(j.pid), e.SessionID))
		}
	case e.Type == "control_request":
		rn.permission(ctx, j, e)
	case e.Type == "result":
		j.mu.Lock()
		j.result = &e
		j.mu.Unlock()

		// One turn per Dispatch for now: closing stdin ends the process.
		_ = shim.Send(ctx, j.dir, []byte(shim.CloseLine))
	case e.Type == "rate_limit_event" && e.RateLimit != nil && e.RateLimit.Status == "rejected":
		t := time.Unix(e.RateLimit.ResetsAt, 0).UTC()

		j.mu.Lock()
		j.limited = &t
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

// permission answers a permission prompt: allowed, for now.
func (rn *Runner) permission(ctx context.Context, j *job, e event) {
	var req struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}

	if json.Unmarshal(e.Request, &req) != nil || req.Subtype != "can_use_tool" {
		return
	}

	rn.Logger.InfoContext(ctx, "tool allowed",
		slog.String("event", "node.tool_allowed"), slog.String("dispatch_id", j.id), slog.String("tool", req.ToolName))

	input := req.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}

	resp, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "success", "request_id": e.RequestID,
			"response": map[string]any{"behavior": "allow", "updatedInput": input},
		},
	})
	if err == nil {
		rn.report(ctx, j, shim.Send(ctx, j.dir, resp))
	}
}

// ended reports the outcome once the worker exited.
func (rn *Runner) ended(ctx context.Context, j *job, exit shim.Exit) {
	rn.drop(j.id)

	j.mu.Lock()
	stopping, started, result, limited := j.stopping, j.started, j.result, j.limited
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
	case !started && result == nil:
		rn.report(ctx, j, j.r.Finished(ctx, nodeproto.Finished{
			Class: "start_failed", Summary: firstLine(exit.Error, rn.stderr(j)), Exit: ex,
		}))

		return
	}

	final, finished, err := bridge.Finished(bridge.Dir(j.dir))
	if err != nil {
		rn.report(ctx, j, err)
	}

	switch {
	case finished:
		f := nodeproto.Finished{Summary: final.Summary, Evidence: final.Evidence, Gate: final.Gate, Exit: ex}
		if result != nil {
			f.Usage = usage(result)
		}

		rn.report(ctx, j, j.r.Finished(ctx, f))
	case result == nil:
		rn.report(ctx, j, j.r.Exited(ctx))
	default:
		rn.report(ctx, j, j.r.Finished(ctx, outcome(result, limited, ex)))
	}
}

func usage(res *event) *nodeproto.Usage {
	if res.Usage == nil {
		return nil
	}

	return &nodeproto.Usage{
		Input: res.Usage.Input, Output: res.Usage.Output, CacheRead: res.Usage.CacheRead, CacheWrite: res.Usage.CacheWrite,
	}
}

// outcome is a worker that exited without calling finish: a rate limit, a
// login, or else a failure (Orca: "Worker stopped without reporting").
func outcome(res *event, limited *time.Time, ex nodeproto.Exit) nodeproto.Finished {
	f := nodeproto.Finished{Summary: res.Result, Exit: ex, Usage: usage(res)}
	lower := strings.ToLower(res.Result)

	switch {
	case limited != nil:
		f.Class, f.ResetsAt = "rate_limited", limited
	case strings.Contains(lower, "invalid api key") || strings.Contains(lower, "please run /login") ||
		strings.Contains(lower, "authentication") || strings.Contains(lower, "oauth token"):
		f.Class = "needs_auth"
	default:
		f.Class = "worker_error"
		f.Summary = strings.TrimSpace("stopped without finish: " + res.Result)
	}

	if f.Summary == "" {
		f.Summary = res.Subtype
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
