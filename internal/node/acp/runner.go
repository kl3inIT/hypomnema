// Package acp runs workers that speak the Agent Client Protocol for the node
// daemon (issue #107): omp and opencode, each an entry of a table
// (agents.go). A worker's model is served by the control plane's model
// gateway, as a Codex worker's.
//
// ACP is JSON-RPC 2.0 over the CLI's standard input and output. A worker runs
// under a shim in its own session, as every worker does, so the conversation
// is written to the shim's input and read from its journal, and a daemon
// that restarts picks the turn up where the journal was last read. That is
// why the few messages are written here and no ACP library holds the
// connection: the references keep theirs in the process that drives the
// agent (Coder's agentacp, OpenHands' ACPAgent, Orca), which ends with it.
// Their sequence and answers are followed
// (docs/research/2026-11-10-acp-omp-opencode.md):
//
//	initialize        no file or terminal capability of the client's (Orca)
//	session/new       with the tools server (ask, finish) as a stdio MCP server (OpenHands)
//	session/resume    for a follow-up, else session/load; a session that is gone starts anew (Coder, OpenHands)
//	session/prompt    one turn; its result ends the process
//	session/request_permission   the first option that allows (Coder), though the configuration asks for none
//
// The worker's transcript is read from the same journal (items.go).
package acp

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
	"github.com/kl3inIT/hypomnema/internal/node/claude"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/skills"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Timers, as the Claude Code runner's.
const (
	progressEvery = 30 * time.Second
	pollEvery     = 200 * time.Millisecond
	socketWait    = 10 * time.Second
	offsetFile    = "offset"
	// stopGrace: a worker asked to stop that is still alive after this is killed.
	stopGrace = 30 * time.Second
)

// The ids of the requests a turn makes, in order.
const (
	idInitialize = 1
	idSession    = 2
	idPrompt     = 3
	// idNewSession is session/new after a session could not be resumed.
	idNewSession = 4
)

// Runner runs ACP workers.
type Runner struct {
	// Home is the node's home: run/<dispatch> holds each shim,
	// conversations/<conversation>/<runtime> each Conversation's directory for the CLI.
	Home string
	// Exe is the node binary, run as `Exe shim --dir D` and `Exe mcp --dir D`.
	Exe string
	// Paths are the CLIs' binaries, by runtime.
	Paths map[string]string
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

	// What the turn needs after the handshake: where the worker works, the
	// session to resume, the prompt. Empty for an adopted worker, whose
	// handshake is past.
	cwd, resume, prompt string

	mu       sync.Mutex
	pid      int
	started  bool
	stopping bool
	// session is the agent's session id; text is the agent's last message.
	session string
	text    strings.Builder
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

// Start prepares the worktree and the Conversation's directory, starts the
// shim, and opens the conversation; the rest follows from the agent's answers.
func (rn *Runner) Start(ctx context.Context, d statedb.Dispatch, r daemon.Reporter) error {
	o := d.Offer

	a, known := agents[o.Runtime]
	path := rn.path(o.Runtime)

	switch {
	case !known:
		return fmt.Errorf("%s is not an ACP agent this node knows", o.Runtime)
	case path == "":
		return fmt.Errorf("%s is not on this node's PATH", o.Runtime)
	case o.ModelKey == "":
		return errors.New("the offer carries no key of the model gateway: " +
			"this worker runs under a profile whose provider is the gateway")
	case o.Model == "":
		return errors.New("the offer names no model")
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

	convDir := filepath.Join(rn.Home, "conversations", o.ConversationID, o.Runtime)

	// The skills the worker is given, placed again at each start so that one taken away is gone (issue #21).
	given := a.skills(convDir)
	if err := skills.Place(rn.Home, given, o.Skills); err != nil {
		return err
	}

	if len(o.Skills) == 0 {
		given = ""
	}

	args, agentEnv, err := a.prepare(launch{
		Dir: convDir, URL: strings.TrimRight(rn.URL, "/") + "/v1", Model: o.Model, Effort: o.Effort, Skills: given,
	})
	if err != nil {
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

	spec := shim.Spec{
		Argv: append([]string{path}, args...), Dir: o.Worktree,
		Env: append(append(rn.env(o.ModelKey), agentEnv...), hostEnv...),
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

	// The client offers no file or terminal of its own (Orca), and no forms:
	// omp would then bring an `ask` of its own beside the tools server's.
	initialize := request(idInitialize, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false,
		},
	})
	if err := shim.Send(ctx, dir, initialize); err != nil {
		_ = shim.Kill(pid)

		return err
	}

	return rn.watch(ctx, &job{
		id: d.ID, dir: dir, r: r, pid: pid, cwd: o.Worktree, resume: session, prompt: claude.Prompt(d),
	})
}

// request is one JSON-RPC request as a line.
func request(id int, method string, params any) []byte {
	//nolint:errchkjson // The parameters are maps of strings, numbers and booleans.
	line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})

	return line
}

// tools is the tools server as a stdio MCP server of a session (OpenHands' McpServerStdio).
func (rn *Runner) tools(dir string) []any {
	return []any{map[string]any{
		"name": "hypomnema", "command": rn.Exe, "args": []string{"mcp", "--dir", bridge.Dir(dir)}, "env": []any{},
	}}
}

// env is the worker's whole environment: a clean one, with the gateway's key.
func (rn *Runner) env(key string) []string {
	out := []string{keyVariable + "=" + key, "LANG=C.UTF-8"}

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
			raw, _ := os.ReadFile(filepath.Join(dir, shim.StderrFile)) //nolint:gosec // the runner's own directory

			return fmt.Errorf("the shim did not open its input socket: %s", strings.TrimSpace(string(raw)))
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

	return rn.watch(ctx, &job{id: d.ID, dir: dir, r: r, pid: pid, started: true})
}

// watch follows the worker until it ends; it outlives the call that started it.
func (rn *Runner) watch(ctx context.Context, j *job) error {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j.cancel = cancel

	if !rn.put(j) {
		cancel()

		return nil
	}

	go rn.tail(ctx, j)

	return nil
}

// Stop cancels the turn and closes the worker's input; one still alive after
// stopGrace is killed.
func (rn *Runner) Stop(id string) {
	j := rn.get(id)
	if j == nil {
		return
	}

	j.mu.Lock()
	already, session := j.stopping, j.session
	j.stopping = true
	j.mu.Unlock()

	if already {
		return
	}

	//nolint:errchkjson // A map of strings.
	cancel, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": session},
	})
	_ = shim.Send(context.Background(), j.dir, cancel, []byte(shim.CloseLine))

	time.AfterFunc(stopGrace, func() {
		if _, done, _ := shim.ReadExit(j.dir); !done && j.pid > 0 {
			_ = shim.Kill(j.pid)
		}
	})
}

// Release kills the worker and forgets it, reporting nothing.
func (rn *Runner) Release(id string) {
	j := rn.drop(id)
	if j == nil {
		return // Not this runner's worker, or gone already.
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

// message is the part of a JSON-RPC line the runner reads.
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
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
	var m message
	if json.Unmarshal(line, &m) != nil {
		return
	}

	id, _ := strconv.Atoi(string(m.ID))

	switch {
	case m.Method == "session/request_permission":
		rn.permit(ctx, j, m)
	case m.Method == "session/update":
		rn.update(ctx, j, m.Params)
	case m.Method != "":
		// Another request or notification of the agent's: the client offered nothing it could call.
	case m.Error != nil:
		rn.refused(ctx, j, id, m.Error.Message+" "+string(m.Error.Data))
	case id == idInitialize:
		rn.openSession(ctx, j, m.Result)
	case id == idSession || id == idNewSession:
		rn.sessionOpened(ctx, j, m.Result)
	case id == idPrompt:
		rn.turnEnded(ctx, j, m.Result)
	}
}

// openSession follows the agent's answer to initialize: a follow-up resumes
// its session (session/resume, which replays nothing; else session/load), a
// first turn opens one.
func (rn *Runner) openSession(ctx context.Context, j *job, result json.RawMessage) {
	var caps struct {
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}

	_ = json.Unmarshal(result, &caps)

	params := map[string]any{"cwd": j.cwd, "mcpServers": rn.tools(j.dir)}
	method := "session/new"
	resumes := len(caps.AgentCapabilities.SessionCapabilities.Resume) > 0 &&
		string(caps.AgentCapabilities.SessionCapabilities.Resume) != "null"

	switch {
	case j.resume == "":
	case resumes:
		method, params["sessionId"] = "session/resume", j.resume
	case caps.AgentCapabilities.LoadSession:
		method, params["sessionId"] = "session/load", j.resume
	}

	rn.report(ctx, j, shim.Send(ctx, j.dir, request(idSession, method, params)))
}

// sessionOpened reports the session and sends the prompt.
func (rn *Runner) sessionOpened(ctx context.Context, j *job, result json.RawMessage) {
	var opened struct {
		SessionID string `json:"sessionId"`
	}

	_ = json.Unmarshal(result, &opened)

	// A resumed session keeps its id; its answer names none.
	session := opened.SessionID
	if session == "" {
		session = j.resume
	}

	j.mu.Lock()
	first := !j.started
	j.started, j.session = true, session
	j.mu.Unlock()

	if first {
		rn.report(ctx, j, j.r.Started(ctx, "shim:"+strconv.Itoa(j.pid), session))
	}

	rn.report(ctx, j, shim.Send(ctx, j.dir, request(idPrompt, "session/prompt", map[string]any{
		"sessionId": session, "prompt": []any{map[string]any{"type": "text", "text": j.prompt}},
	})))
}

// turnEnded records how the turn ended and closes the agent's input, which ends it.
func (rn *Runner) turnEnded(ctx context.Context, j *job, result json.RawMessage) {
	var ended struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			Input      int64 `json:"inputTokens"`
			Output     int64 `json:"outputTokens"`
			CacheRead  int64 `json:"cachedReadTokens"`
			CacheWrite int64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}

	_ = json.Unmarshal(result, &ended)

	j.mu.Lock()
	j.done = true

	if ended.StopReason != "end_turn" {
		j.failure = "the turn ended with " + ended.StopReason
	}

	if u := ended.Usage; u != nil {
		j.usage = &nodeproto.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
	}
	j.mu.Unlock()

	rn.report(ctx, j, shim.Send(ctx, j.dir, []byte(shim.CloseLine)))
}

// refused handles an error answer. A session that cannot be resumed is opened
// anew (OpenHands falls back to a new session); any other ends the turn.
func (rn *Runner) refused(ctx context.Context, j *job, id int, text string) {
	if id == idSession && j.resume != "" {
		rn.Logger.WarnContext(ctx, "a session could not be resumed; a new one is opened",
			slog.String("event", "node.acp_resume_failed"), slog.String("dispatch_id", j.id),
			slog.String("error", strings.TrimSpace(text)))

		rn.report(ctx, j, shim.Send(ctx, j.dir, request(idNewSession, "session/new", map[string]any{
			"cwd": j.cwd, "mcpServers": rn.tools(j.dir),
		})))

		return
	}

	j.mu.Lock()
	j.failure = strings.TrimSpace(text)
	j.mu.Unlock()

	rn.report(ctx, j, shim.Send(ctx, j.dir, []byte(shim.CloseLine)))
}

// permit answers a permission request with the first option that allows, for
// good if there is one (Coder's client). The agents are configured to ask for
// none; one that comes is allowed and logged, as Claude Code's runner does.
func (rn *Runner) permit(ctx context.Context, j *job, m message) {
	var req struct {
		ToolCall struct {
			Title string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}

	if json.Unmarshal(m.Params, &req) != nil || len(m.ID) == 0 {
		return
	}

	chosen := ""

	for _, kind := range []string{"allow_always", "allow_once"} {
		for _, o := range req.Options {
			if o.Kind == kind && chosen == "" {
				chosen = o.OptionID
			}
		}
	}

	outcome := map[string]any{"outcome": "cancelled"} //nolint:misspell // ACP's own spelling
	if chosen != "" {
		outcome = map[string]any{"outcome": "selected", "optionId": chosen}
	}

	rn.Logger.InfoContext(ctx, "tool allowed",
		slog.String("event", "node.tool_allowed"), slog.String("dispatch_id", j.id),
		slog.String("tool", req.ToolCall.Title))

	//nolint:errchkjson // A map of strings and the request's own id.
	answer, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": outcome},
	})
	rn.report(ctx, j, shim.Send(ctx, j.dir, answer))
}

// update keeps the agent's last message, which is where omp says what went
// wrong with the model, and reports progress.
func (rn *Runner) update(ctx context.Context, j *job, params json.RawMessage) {
	var u struct {
		Update struct {
			Kind    string `json:"sessionUpdate"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}

	_ = json.Unmarshal(params, &u)

	j.mu.Lock()

	switch u.Update.Kind {
	case "agent_message_chunk":
		j.text.WriteString(u.Update.Content.Text)
	case "tool_call":
		j.text.Reset() // What was said before a tool was called is not the last word.
	}

	due := time.Since(j.progress) >= progressEvery
	if due {
		j.progress = time.Now()
	}
	j.mu.Unlock()

	if due {
		rn.report(ctx, j, j.r.Progress(ctx))
	}
}

// ended reports the outcome once the worker exited.
func (rn *Runner) ended(ctx context.Context, j *job, exit shim.Exit) {
	rn.drop(j.id)

	j.mu.Lock()
	stopping, started, done, failure, used := j.stopping, j.started, j.done, j.failure, j.usage
	said := strings.TrimSpace(j.text.String())
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
		// omp ends a turn the model refused as any other, and says why in its message.
		if failure == "" {
			failure = firstLine(said)
		}

		rn.report(ctx, j, j.r.Finished(ctx, outcome(failure, ex, used)))
	}
}

// outcome is a worker that exited without calling finish: the gateway's key
// or the account behind it refused, a limit (opencode names it
// provider.rate-limit), or else a failure.
func outcome(failure string, ex nodeproto.Exit, used *nodeproto.Usage) nodeproto.Finished {
	f := nodeproto.Finished{Summary: failure, Exit: ex, Usage: used}
	lower := strings.ToLower(failure)

	switch {
	case strings.Contains(lower, "usage limit") || strings.Contains(lower, "429") ||
		strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate-limit") ||
		strings.Contains(lower, "quota"):
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

// path is a CLI's binary, "" when the node has none.
func (rn *Runner) path(runtime string) string {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	return rn.Paths[runtime]
}

// SetPaths sets the CLIs' binaries, found again once the control plane had the node install one (issue #107).
func (rn *Runner) SetPaths(paths map[string]string) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.Paths = paths
}
