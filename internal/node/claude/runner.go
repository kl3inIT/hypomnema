// Package claude is the runtime of Claude Code workers for the node daemon
// (issue #32). A worker's lifecycle is the one every runtime shares
// (internal/node/runner): a shim, its journal, and the two tools, ask and
// finish, over the bridge directory. The runtime drives the CLI over
// bidirectional stream-JSON, the protocol of spike B
// (docs/research/2026-11-06-headless-cli-drivers.md):
//
//	claude -p --input-format stream-json --output-format stream-json --verbose
//	  --permission-mode manual --permission-prompt-tool stdio [--resume <session>] [--model <m>]
//
// It reports the session id from system/init. Every permission
// prompt is allowed and logged: the node's machine or user is the boundary,
// as Orca, OpenHands and Coder run their workers by default.
package claude

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
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/skills"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

const (
	mcpFile = "mcp.json"
	// toolTimeout is the hard limit on one tool call, so an ask can wait a day
	// (spike 2: the default is about 27.8 h; set explicitly).
	toolTimeout = 24 * time.Hour
	// disallowedTools are the tools a worker does not get: questions go through
	// ask, and a worker does not start subagents (the tool is Task in older
	// Claude Code, Agent in newer). The list keeps it, not the instructions
	// (docs/research/2026-11-09-agent-roles-and-tool-limits.md).
	disallowedTools = "AskUserQuestion,Task,Agent"
)

// Account is how the node runs Claude under one account when the control
// plane sends no token with the offer: the file holding its `claude
// setup-token` token.
type Account struct {
	TokenFile string `json:"token_file"`
}

// ClaudeRuntime runs Claude Code workers.
type ClaudeRuntime struct {
	// Home is the node's home: accounts/<id> holds each account's config dir.
	Home string
	// Exe is the node binary, run as `Exe mcp --dir D`.
	Exe string
	// Claude is the claude binary.
	Claude string
	// URL is the control plane's base URL, where a worker's tools ask for a token.
	URL      string
	Accounts map[string]Account
	// Env is added to each worker's environment (tests).
	Env []string
}

var _ runner.Runtime = (*ClaudeRuntime)(nil)

// Prepare checks the offer and readies the account's config dir; the worker
// gets the initialize request and the first turn.
func (rt *ClaudeRuntime) Prepare(_ context.Context, d statedb.Dispatch) (runner.Launch, error) {
	o := d.Offer

	if rt.Claude == "" {
		return runner.Launch{}, errors.New("claude is not on this node's PATH")
	}

	credentials, err := rt.credentials(o)
	if err != nil {
		return runner.Launch{}, err
	}

	configDir := filepath.Join(rt.Home, "accounts", o.AccountRef, "claude")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return runner.Launch{}, fmt.Errorf("making the account's config dir: %w", err)
	}

	write := func(dir string, hostEnv []string) (shim.Spec, error) {
		argv := []string{rt.Claude, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--permission-mode", "manual", "--permission-prompt-tool", "stdio",
			"--mcp-config", filepath.Join(dir, mcpFile), "--strict-mcp-config", "--disallowedTools", disallowedTools}
		if session := runner.Session(d); session != "" {
			argv = append(argv, "--resume", session)
		}

		if o.Model != "" {
			argv = append(argv, "--model", o.Model)
		}

		// The profile's effort, in Claude Code's own names, which are the same
		// five (OpenMausBot's and t3code's drivers pass it the same way).
		if o.Effort != "" {
			argv = append(argv, "--effort", o.Effort)
		}

		// The skills the worker is given are a plugin of its own (issue #21).
		if len(o.Skills) > 0 {
			plugin := filepath.Join(dir, "plugin")
			if err := skills.PlacePlugin(rt.Home, plugin, o.Skills); err != nil {
				return shim.Spec{}, err
			}

			argv = append(argv, "--plugin-dir", plugin)
		}

		toolEnv, err := agenttool.ForDispatch(rt.Home, rt.Exe, rt.URL, o.Uses, o.AgentKey)
		if err != nil {
			return shim.Spec{}, err
		}

		if err := rt.writeMCPConfig(dir); err != nil {
			return shim.Spec{}, err
		}

		return shim.Spec{Argv: argv, Env: append(append(rt.env(configDir, credentials), hostEnv...), toolEnv...)}, nil
	}

	first, err := json.Marshal(map[string]any{
		"type": "user", "message": map[string]any{"role": "user", "content": runner.Prompt(d)},
	})
	if err != nil {
		return runner.Launch{}, err
	}

	initialize := []byte(`{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}`)

	return runner.Launch{Format: Format, Write: write, First: [][]byte{initialize, first}, Turn: &turn{}}, nil
}

// Adopted is a turn read on from where an earlier daemon left it, with what
// it had learnt by then.
func (rt *ClaudeRuntime) Adopted(state []byte) runner.Turn {
	t := &turn{}

	var s saved
	if json.Unmarshal(state, &s) == nil {
		t.result, t.limited = s.Result, s.Limited
	}

	return t
}

// saved is what a turn keeps across a daemon restart: the result event and
// a rate limit's reset.
type saved struct {
	Result  *event     `json:"result,omitempty"`
	Limited *time.Time `json:"limited,omitempty"`
}

// gatewayAccount is the offer's AccountRef when the model gateway serves the
// worker's model and no account on the node does (issue #107).
const gatewayAccount = "gateway"

// credentials is how the worker reaches Claude (issue #381). Under the
// gateway it calls the control plane's /v1/messages with its Dispatch's key,
// as Claude Code is pointed at an LLM gateway (its docs, llm-gateway-connect:
// ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN), and as the Codex runtime calls
// the gateway. It asks for the one-hour prompt cache for its main
// conversation, which Claude Code gives a subscription of its own but not a
// gateway's credential (prompt-caching: "set promptCacheTtl to 1h"; omp's
// getCacheControl gives a subscription the hour too); subagents keep Claude
// Code's five minutes, as CLIProxyAPI leaves them. The key is good for the
// offer's model alone, so every model Claude Code names by family is pinned
// to it (llm-gateway-connect: pin ANTHROPIC_DEFAULT_*_MODEL). Otherwise the
// worker runs under the account's token: it comes with the offer when the
// control plane keeps the account (issue #150), else from the file this
// node's configuration names.
func (rt *ClaudeRuntime) credentials(o nodeproto.Offer) ([]string, error) {
	if o.AccountRef == gatewayAccount {
		switch {
		case o.ModelKey == "":
			return nil, errors.New("the offer carries no key of the model gateway")
		case rt.URL == "":
			return nil, errors.New("the node does not know its control plane's URL")
		}

		out := []string{
			"ANTHROPIC_BASE_URL=" + strings.TrimRight(rt.URL, "/"), "ANTHROPIC_AUTH_TOKEN=" + o.ModelKey,
			"CLAUDE_CODE_PROMPT_CACHE_TTL=1h",
		}
		if o.Model != "" {
			out = append(out, "ANTHROPIC_DEFAULT_OPUS_MODEL="+o.Model, "ANTHROPIC_DEFAULT_SONNET_MODEL="+o.Model,
				"ANTHROPIC_DEFAULT_HAIKU_MODEL="+o.Model)
		}

		return out, nil
	}

	token := []byte(o.AccountToken)

	if len(token) == 0 {
		acc, ok := rt.Accounts[o.AccountRef]
		if !ok {
			return nil, fmt.Errorf("the node has no credentials for account %s", o.AccountRef)
		}

		var err error

		if token, err = os.ReadFile(acc.TokenFile); err != nil {
			return nil, fmt.Errorf("reading the token of account %s: %w", o.AccountRef, err)
		}
	}

	return []string{"CLAUDE_CODE_OAUTH_TOKEN=" + strings.TrimSpace(string(token))}, nil
}

// env is the worker's whole environment: a clean one (durable-harness
// env.ts) with how it reaches Claude and its own config dir (Orca's
// CLAUDE_CONFIG_DIR per account). Claude Code's own auto-memory is off: it is
// kept per working folder, and a worker's is a new worktree per Task, so its
// notes would be split between them and the folder's path would change the
// system prompt the provider caches; the agent's memory is Hypomnema's
// (issue #21), as OpenMausBot turns it off for its bots
// (server/drivers/claude-own-memory.ts).
func (rt *ClaudeRuntime) env(configDir string, credentials []string) []string {
	out := append([]string{"CLAUDE_CONFIG_DIR=" + configDir}, credentials...)
	out = append(out, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		"LANG=C.UTF-8", "MCP_TOOL_TIMEOUT="+strconv.FormatInt(toolTimeout.Milliseconds(), 10))

	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SystemRoot", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}

	return append(out, rt.Env...)
}

// writeMCPConfig names the worker's tools server: this binary's `mcp`
// command over the bridge directory.
func (rt *ClaudeRuntime) writeMCPConfig(dir string) error {
	raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"hypomnema": map[string]any{
			"type": "stdio", "command": rt.Exe, "args": []string{"mcp", "--dir", bridge.Dir(dir)},
			"timeout": toolTimeout.Milliseconds(),
		},
	}})
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, mcpFile), raw, 0o600)
}

// event is the part of a stream-JSON line the runtime reads.
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

// turn is one stream-JSON turn. Its lines come from one goroutine, and it is
// read once the worker exited.
type turn struct {
	result  *event
	limited *time.Time
}

func (t *turn) Line(ctx context.Context, w *runner.Worker, line []byte) {
	var e event
	if json.Unmarshal(line, &e) != nil {
		return
	}

	switch {
	case e.Type == "system" && e.Subtype == "init":
		w.Started(ctx, e.SessionID)
	case e.Type == "control_request":
		permission(ctx, w, e)
	case e.Type == "result":
		t.result = &e

		// One turn per Dispatch for now: closing stdin ends the process.
		_ = w.Send(ctx, []byte(shim.CloseLine))
	case e.Type == "rate_limit_event" && e.RateLimit != nil && e.RateLimit.Status == "rejected":
		limited := time.Unix(e.RateLimit.ResetsAt, 0).UTC()
		t.limited = &limited
	}
}

// permission answers a permission prompt: allowed, for now.
func permission(ctx context.Context, w *runner.Worker, e event) {
	var req struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}

	if json.Unmarshal(e.Request, &req) != nil || req.Subtype != "can_use_tool" {
		return
	}

	w.Logger().InfoContext(ctx, "tool allowed",
		slog.String("event", "node.tool_allowed"), slog.String("dispatch_id", w.ID()), slog.String("tool", req.ToolName))

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
		w.Report(ctx, w.Send(ctx, resp))
	}
}

// Interrupt asks Claude Code to stop its turn.
func (t *turn) Interrupt() []byte {
	return []byte(`{"type":"control_request","request_id":"stop","request":{"subtype":"interrupt"}}`)
}

func (t *turn) Result() runner.Result {
	if t.result == nil {
		return runner.Result{}
	}

	return runner.Result{Done: true, Usage: usage(t.result)}
}

func (t *turn) State() []byte {
	//nolint:errchkjson // The event as it was decoded, and a time.
	raw, _ := json.Marshal(saved{Result: t.result, Limited: t.limited})

	return raw
}

func usage(res *event) *nodeproto.Usage {
	if res.Usage == nil {
		return nil
	}

	return &nodeproto.Usage{
		Input: res.Usage.Input, Output: res.Usage.Output, CacheRead: res.Usage.CacheRead, CacheWrite: res.Usage.CacheWrite,
	}
}

// Outcome is a worker that exited with a result but without calling finish:
// a rate limit, a login, or else a failure (Orca: "Worker stopped without
// reporting").
func (t *turn) Outcome(ex nodeproto.Exit) nodeproto.Finished {
	res := t.result
	f := nodeproto.Finished{Summary: res.Result, Exit: ex, Usage: usage(res)}
	lower := strings.ToLower(res.Result)

	switch {
	case t.limited != nil:
		f.Class, f.ResetsAt = "rate_limited", t.limited
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
