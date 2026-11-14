// Package codex is the runtime of Codex CLI workers for the node daemon
// (issue #107). A worker's model is served by the control plane's model
// gateway, under an account logged in there: no ChatGPT login exists on the
// node.
//
// A worker's lifecycle is the one every runtime shares (internal/node/runner):
// a shim, its journal, and the two tools, ask and finish, over the bridge
// directory. What differs is the CLI:
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// toolTimeout lets an ask wait a day for its answer.
const toolTimeout = 24 * time.Hour

const (
	// providerID names the gateway in the CLI's configuration.
	providerID = "hypomnema"
	// keyVariable is the environment variable the CLI reads the gateway's key from.
	keyVariable = "HYPOMNEMA_MODEL_KEY"
)

// CodexRuntime runs Codex CLI workers.
type CodexRuntime struct {
	// Home is the node's home: conversations/<conversation>/codex holds each
	// Conversation's CODEX_HOME.
	Home string
	// Exe is the node binary, run as `Exe mcp --dir D`.
	Exe string
	// Codex is the codex binary.
	Codex string
	// URL is the control plane's base URL; the gateway is at URL/v1.
	URL string
	// Env is added to each worker's environment (tests).
	Env []string
}

var _ runner.Runtime = (*CodexRuntime)(nil)

// quote writes a string as a TOML value for a -c override.
func quote(s string) string { return strconv.Quote(s) }

// argv is the command of one turn.
func (rt *CodexRuntime) argv(o nodeproto.Offer, dir, session string) []string {
	provider := "model_providers." + providerID
	tools := "mcp_servers.hypomnema"

	args, _ := json.Marshal([]string{"mcp", "--dir", bridge.Dir(dir)}) //nolint:errchkjson // Strings always encode.

	argv := []string{
		rt.Codex, "exec", "--json", "--skip-git-repo-check", "--ignore-user-config",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c", "model_provider=" + quote(providerID),
		"-c", provider + ".name=" + quote("Hypomnema"),
		"-c", provider + ".base_url=" + quote(strings.TrimRight(rt.URL, "/")+"/v1"),
		"-c", provider + ".env_key=" + quote(keyVariable),
		"-c", provider + ".wire_api=" + quote("responses"),
		"-c", provider + ".supports_websockets=false",
		// The gateway answers Codex's standalone search, its web.run tool, at <base URL>/alpha/search (issue #354);
		// Codex offers the tool only to a provider that says so (codex-rs ext/web-search/src/extension.rs), as
		// OpenClaw sets it for a provider that serves the endpoint (docs/plugins/codex-harness/runtime-behavior.md).
		"-c", provider + ".supports_standalone_web_search=true",
		"-c", tools + ".command=" + quote(rt.Exe),
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

// Prepare checks the offer and readies the Conversation's CODEX_HOME; the
// worker gets the prompt, then the end of its input.
func (rt *CodexRuntime) Prepare(_ context.Context, d statedb.Dispatch) (runner.Launch, error) {
	o := d.Offer

	switch {
	case rt.Codex == "":
		return runner.Launch{}, errors.New("codex is not on this node's PATH")
	case o.ModelKey == "":
		return runner.Launch{}, errors.New("the offer carries no key of the model gateway: " +
			"a Codex worker runs under a profile whose provider is the gateway")
	case rt.URL == "":
		return runner.Launch{}, errors.New("the node does not know its control plane's URL")
	}

	codexHome := filepath.Join(rt.Home, "conversations", o.ConversationID, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return runner.Launch{}, fmt.Errorf("making the conversation's CODEX_HOME: %w", err)
	}

	// The skills the worker is given, placed again at each start so that one taken away is gone (issue #21).
	if err := skills.Place(rt.Home, filepath.Join(codexHome, "skills"), o.Skills); err != nil {
		return runner.Launch{}, err
	}

	write := func(dir string, hostEnv []string) (shim.Spec, error) {
		toolEnv, err := agenttool.ForDispatch(rt.Home, rt.Exe, rt.URL, o.Uses, o.AgentKey)
		if err != nil {
			return shim.Spec{}, err
		}

		return shim.Spec{
			Argv: rt.argv(o, dir, runner.Session(d)),
			Env:  append(append(rt.env(codexHome, o.ModelKey), hostEnv...), toolEnv...),
		}, nil
	}

	// The whole prompt, then the end of input: exec reads it to the end.
	return runner.Launch{
		Format: Format, Write: write, First: [][]byte{[]byte(runner.Prompt(d)), []byte(shim.CloseLine)},
		Turn: &turn{},
	}, nil
}

// Adopted is a turn read on from where an earlier daemon left it, with what
// it had learnt by then.
func (rt *CodexRuntime) Adopted(state []byte) runner.Turn {
	t := &turn{}

	var s saved
	if json.Unmarshal(state, &s) == nil {
		t.done, t.failure, t.usage = s.Done, s.Failure, s.Usage
	}

	return t
}

// saved is what a turn keeps across a daemon restart.
type saved struct {
	Done    bool             `json:"done,omitempty"`
	Failure string           `json:"failure,omitempty"`
	Usage   *nodeproto.Usage `json:"usage,omitempty"`
}

// env is the worker's whole environment: a clean one, with the gateway's key
// and the Conversation's CODEX_HOME.
func (rt *CodexRuntime) env(codexHome, key string) []string {
	out := []string{"CODEX_HOME=" + codexHome, keyVariable + "=" + key, "LANG=C.UTF-8"}

	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SystemRoot", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}

	return append(out, rt.Env...)
}

// event is the part of a line of `codex exec --json` the runtime reads
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

// turn is one turn of `codex exec`. Its lines come from one goroutine, and
// it is read once the worker exited.
type turn struct {
	// done says a turn completed; failure is what the CLI said when one did not.
	done    bool
	failure string
	usage   *nodeproto.Usage
}

func (t *turn) Line(ctx context.Context, w *runner.Worker, line []byte) {
	var e event
	if json.Unmarshal(line, &e) != nil {
		return
	}

	switch e.Type {
	case "thread.started":
		w.Started(ctx, e.ThreadID)
	case "turn.completed":
		// An error before this was one the CLI got over: it prints "Reconnecting…" as an error event.
		t.done, t.failure = true, ""

		if e.Usage != nil {
			// Codex counts the cached part inside the input.
			t.usage = &nodeproto.Usage{
				Input: max(e.Usage.Input-e.Usage.Cached, 0), Output: e.Usage.Output, CacheRead: e.Usage.Cached,
			}
		}
	case "turn.failed", "error":
		t.failure = e.Message
		if e.Error != nil && e.Error.Message != "" {
			t.failure = e.Error.Message
		}
	}
}

// Interrupt is nil: exec has no way to be interrupted, so its process is killed.
func (t *turn) Interrupt() []byte { return nil }

func (t *turn) Result() runner.Result {
	return runner.Result{Done: t.done, Failure: t.failure, Usage: t.usage}
}

func (t *turn) State() []byte {
	//nolint:errchkjson // A bool, a string and numbers.
	raw, _ := json.Marshal(saved{Done: t.done, Failure: t.failure, Usage: t.usage})

	return raw
}

// Outcome is a worker that exited without calling finish: the gateway's key
// or the account behind it refused, a limit, or else a failure.
func (t *turn) Outcome(ex nodeproto.Exit) nodeproto.Finished {
	f := nodeproto.Finished{Summary: t.failure, Exit: ex, Usage: t.usage}
	lower := strings.ToLower(t.failure)

	switch {
	case strings.Contains(lower, "usage limit") || strings.Contains(lower, "429") ||
		strings.Contains(lower, "rate limit"):
		f.Class = "rate_limited"
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid_api_key"):
		f.Class = "needs_auth"
	default:
		f.Class = "worker_error"
		f.Summary = strings.TrimSpace("stopped without finish: " + t.failure)
	}

	return f
}
