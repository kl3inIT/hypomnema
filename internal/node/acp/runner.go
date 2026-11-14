// Package acp is the runtime of workers that speak the Agent Client Protocol
// for the node daemon (issue #107): omp and opencode, each an entry of a
// table (agents.go). A worker's model is served by the control plane's model
// gateway, as a Codex worker's.
//
// ACP is JSON-RPC 2.0 over the CLI's standard input and output. A worker runs
// under a shim in its own session, as every worker does (internal/node/runner),
// so the conversation is written to the shim's input and read from its
// journal, and a daemon that restarts picks the turn up where the journal was
// last read. That is why the few messages are written here and no ACP library
// holds the connection: the references keep theirs in the process that
// drives the agent (Coder's agentacp, OpenHands' ACPAgent, Orca), which ends
// with it. Their sequence and answers are followed
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/skills"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The ids of the requests a turn makes, in order.
const (
	idInitialize = 1
	idSession    = 2
	idPrompt     = 3
	// idNewSession is session/new after a session could not be resumed.
	idNewSession = 4
)

// ACPRuntime runs ACP workers.
type ACPRuntime struct {
	// Home is the node's home: conversations/<conversation>/<runtime> holds
	// each Conversation's directory for the CLI.
	Home string
	// Exe is the node binary, run as `Exe mcp --dir D`.
	Exe string
	// Paths are the CLIs' binaries, by runtime.
	Paths map[string]string
	// URL is the control plane's base URL; the gateway is at URL/v1.
	URL string
	// Env is added to each worker's environment (tests).
	Env []string

	mu sync.Mutex
}

var _ runner.Runtime = (*ACPRuntime)(nil)

// Prepare checks the offer and writes the CLI's configuration in the
// Conversation's directory; the turn opens the conversation, and the rest
// follows from the agent's answers.
func (rt *ACPRuntime) Prepare(_ context.Context, d statedb.Dispatch) (runner.Launch, error) {
	o := d.Offer

	a, known := agents[o.Runtime]
	path := rt.path(o.Runtime)

	switch {
	case !known:
		return runner.Launch{}, fmt.Errorf("%s is not an ACP agent this node knows", o.Runtime)
	case path == "":
		return runner.Launch{}, fmt.Errorf("%s is not on this node's PATH", o.Runtime)
	case o.ModelKey == "":
		return runner.Launch{}, errors.New("the offer carries no key of the model gateway: " +
			"this worker runs under a profile whose provider is the gateway")
	case o.Model == "":
		return runner.Launch{}, errors.New("the offer names no model")
	case rt.URL == "":
		return runner.Launch{}, errors.New("the node does not know its control plane's URL")
	}

	convDir := filepath.Join(rt.Home, "conversations", o.ConversationID, o.Runtime)

	// The skills the worker is given, placed again at each start so that one taken away is gone (issue #21).
	given := a.skills(convDir)
	if err := skills.Place(rt.Home, given, o.Skills); err != nil {
		return runner.Launch{}, err
	}

	if len(o.Skills) == 0 {
		given = ""
	}

	args, agentEnv, err := a.prepare(launch{
		Dir: convDir, URL: strings.TrimRight(rt.URL, "/") + "/v1", Model: o.Model, Effort: o.Effort, Skills: given,
	})
	if err != nil {
		return runner.Launch{}, err
	}

	write := func(_ string, hostEnv []string) (shim.Spec, error) {
		return shim.Spec{
			Argv: append([]string{path}, args...), Env: append(append(rt.env(o.ModelKey), agentEnv...), hostEnv...),
		}, nil
	}

	// The client offers no file or terminal of its own (Orca), and no forms:
	// omp would then bring an `ask` of its own beside the tools server's.
	initialize := request(idInitialize, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false,
		},
	})

	return runner.Launch{
		Format: Format, Write: write, First: [][]byte{initialize},
		Turn: &turn{rt: rt, cwd: o.Worktree, resume: runner.Session(d), prompt: runner.Prompt(d)},
	}, nil
}

// Adopted is a turn whose handshake is past, with what it had learnt by
// then: its session above all, which a stop cancels.
func (rt *ACPRuntime) Adopted(state []byte) runner.Turn {
	t := &turn{rt: rt}

	var s saved
	if json.Unmarshal(state, &s) == nil {
		t.session, t.done, t.failure, t.usage = s.Session, s.Done, s.Failure, s.Usage
		t.text.WriteString(s.Text)
	}

	return t
}

// saved is what a turn keeps across a daemon restart.
type saved struct {
	Session string           `json:"session,omitempty"`
	Text    string           `json:"text,omitempty"`
	Done    bool             `json:"done,omitempty"`
	Failure string           `json:"failure,omitempty"`
	Usage   *nodeproto.Usage `json:"usage,omitempty"`
}

// request is one JSON-RPC request as a line.
func request(id int, method string, params any) []byte {
	//nolint:errchkjson // The parameters are maps of strings, numbers and booleans.
	line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})

	return line
}

// tools is the tools server as a stdio MCP server of a session (OpenHands' McpServerStdio).
func (rt *ACPRuntime) tools(dir string) []any {
	return []any{map[string]any{
		"name": "hypomnema", "command": rt.Exe, "args": []string{"mcp", "--dir", bridge.Dir(dir)}, "env": []any{},
	}}
}

// env is the worker's whole environment: a clean one, with the gateway's key.
func (rt *ACPRuntime) env(key string) []string {
	out := []string{keyVariable + "=" + key, "LANG=C.UTF-8"}

	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SystemRoot", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}

	return append(out, rt.Env...)
}

// path is a CLI's binary, "" when the node has none.
func (rt *ACPRuntime) path(runtime string) string {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	return rt.Paths[runtime]
}

// SetPaths sets the CLIs' binaries, found again once the control plane had the node install one (issue #107).
func (rt *ACPRuntime) SetPaths(paths map[string]string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.Paths = paths
}

// message is the part of a JSON-RPC line the runtime reads.
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

// turn is one ACP turn. Its lines come from one goroutine; a stop reads its
// session from another, so its state is under mu.
type turn struct {
	rt *ACPRuntime

	// What the turn needs after the handshake: where the worker works, the
	// session to resume, the prompt. Empty for an adopted worker, whose
	// handshake is past.
	cwd, resume, prompt string

	mu sync.Mutex
	// session is the agent's session id; text is the agent's last message.
	session string
	text    strings.Builder
	// done says a turn completed; failure is what the CLI said when one did not.
	done    bool
	failure string
	usage   *nodeproto.Usage
}

func (t *turn) Line(ctx context.Context, w *runner.Worker, line []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil {
		return
	}

	id, _ := strconv.Atoi(string(m.ID))

	switch {
	case m.Method == "session/request_permission":
		t.permit(ctx, w, m)
	case m.Method == "session/update":
		t.update(ctx, w, m.Params)
	case m.Method != "":
		// Another request or notification of the agent's: the client offered nothing it could call.
	case m.Error != nil:
		t.refused(ctx, w, id, m.Error.Message+" "+string(m.Error.Data))
	case id == idInitialize:
		t.openSession(ctx, w, m.Result)
	case id == idSession || id == idNewSession:
		t.sessionOpened(ctx, w, m.Result)
	case id == idPrompt:
		t.turnEnded(ctx, w, m.Result)
	}
}

// Interrupt cancels the turn.
func (t *turn) Interrupt() []byte {
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()

	//nolint:errchkjson // A map of strings.
	cancel, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": session},
	})

	return cancel
}

func (t *turn) Result() runner.Result {
	t.mu.Lock()
	defer t.mu.Unlock()

	return runner.Result{Done: t.done, Failure: t.failure, Usage: t.usage}
}

func (t *turn) State() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	//nolint:errchkjson // Strings, a bool and numbers.
	raw, _ := json.Marshal(saved{
		Session: t.session, Text: t.text.String(), Done: t.done, Failure: t.failure, Usage: t.usage,
	})

	return raw
}

// openSession follows the agent's answer to initialize: a follow-up resumes
// its session (session/resume, which replays nothing; else session/load), a
// first turn opens one.
func (t *turn) openSession(ctx context.Context, w *runner.Worker, result json.RawMessage) {
	var caps struct {
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}

	_ = json.Unmarshal(result, &caps)

	params := map[string]any{"cwd": t.cwd, "mcpServers": t.rt.tools(w.Dir())}
	method := "session/new"
	resumes := len(caps.AgentCapabilities.SessionCapabilities.Resume) > 0 &&
		string(caps.AgentCapabilities.SessionCapabilities.Resume) != "null"

	switch {
	case t.resume == "":
	case resumes:
		method, params["sessionId"] = "session/resume", t.resume
	case caps.AgentCapabilities.LoadSession:
		method, params["sessionId"] = "session/load", t.resume
	}

	w.Report(ctx, w.Send(ctx, request(idSession, method, params)))
}

// sessionOpened reports the session and sends the prompt.
func (t *turn) sessionOpened(ctx context.Context, w *runner.Worker, result json.RawMessage) {
	var opened struct {
		SessionID string `json:"sessionId"`
	}

	_ = json.Unmarshal(result, &opened)

	// A resumed session keeps its id; its answer names none.
	session := opened.SessionID
	if session == "" {
		session = t.resume
	}

	t.mu.Lock()
	t.session = session
	t.mu.Unlock()

	w.Started(ctx, session)

	w.Report(ctx, w.Send(ctx, request(idPrompt, "session/prompt", map[string]any{
		"sessionId": session, "prompt": []any{map[string]any{"type": "text", "text": t.prompt}},
	})))
}

// turnEnded records how the turn ended and closes the agent's input, which ends it.
func (t *turn) turnEnded(ctx context.Context, w *runner.Worker, result json.RawMessage) {
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

	t.mu.Lock()
	t.done = true

	if ended.StopReason != "end_turn" {
		t.failure = "the turn ended with " + ended.StopReason
	}

	if u := ended.Usage; u != nil {
		t.usage = &nodeproto.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
	}
	t.mu.Unlock()

	w.Report(ctx, w.Send(ctx, []byte(shim.CloseLine)))
}

// refused handles an error answer. A session that cannot be resumed is opened
// anew (OpenHands falls back to a new session); any other ends the turn.
func (t *turn) refused(ctx context.Context, w *runner.Worker, id int, text string) {
	if id == idSession && t.resume != "" {
		w.Logger().WarnContext(ctx, "a session could not be resumed; a new one is opened",
			slog.String("event", "node.acp_resume_failed"), slog.String("dispatch_id", w.ID()),
			slog.String("error", strings.TrimSpace(text)))

		w.Report(ctx, w.Send(ctx, request(idNewSession, "session/new", map[string]any{
			"cwd": t.cwd, "mcpServers": t.rt.tools(w.Dir()),
		})))

		return
	}

	t.mu.Lock()
	t.failure = strings.TrimSpace(text)
	t.mu.Unlock()

	w.Report(ctx, w.Send(ctx, []byte(shim.CloseLine)))
}

// permit answers a permission request with the first option that allows, for
// good if there is one (Coder's client). The agents are configured to ask for
// none; one that comes is allowed and logged, as Claude Code's runtime does.
func (t *turn) permit(ctx context.Context, w *runner.Worker, m message) {
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

	w.Logger().InfoContext(ctx, "tool allowed",
		slog.String("event", "node.tool_allowed"), slog.String("dispatch_id", w.ID()),
		slog.String("tool", req.ToolCall.Title))

	//nolint:errchkjson // A map of strings and the request's own id.
	answer, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": outcome},
	})
	w.Report(ctx, w.Send(ctx, answer))
}

// update keeps the agent's last message, which is where omp says what went
// wrong with the model, and reports progress.
func (t *turn) update(ctx context.Context, w *runner.Worker, params json.RawMessage) {
	var u struct {
		Update struct {
			Kind    string `json:"sessionUpdate"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}

	_ = json.Unmarshal(params, &u)

	t.mu.Lock()

	switch u.Update.Kind {
	case "agent_message_chunk":
		t.text.WriteString(u.Update.Content.Text)
	case "tool_call":
		t.text.Reset() // What was said before a tool was called is not the last word.
	}
	t.mu.Unlock()

	w.Progress(ctx)
}

// Outcome is a worker that exited without calling finish: the gateway's key
// or the account behind it refused, a limit (opencode names it
// provider.rate-limit), or else a failure. omp ends a turn the model refused
// as any other, and says why in its message.
func (t *turn) Outcome(ex nodeproto.Exit) nodeproto.Finished {
	t.mu.Lock()
	failure, used, said := t.failure, t.usage, strings.TrimSpace(t.text.String())
	t.mu.Unlock()

	if failure == "" {
		failure = runner.FirstLine(said)
	}

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
