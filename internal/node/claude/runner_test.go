package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/node/workertools"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The test binary also runs the shim, the MCP server, and a fake Claude (FAKE_CLAUDE set).
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shim" && os.Args[2] == "--dir" {
		if err := shim.Main(context.Background(), os.Args[3]); err != nil {
			os.Exit(2)
		}

		os.Exit(0)
	}

	if len(os.Args) == 4 && os.Args[1] == "mcp" && os.Args[2] == "--dir" {
		if err := workertools.Serve(context.Background(), os.Stdin, os.Stdout, os.Args[3]); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}

	if mode := os.Getenv("FAKE_CLAUDE"); mode != "" {
		os.Exit(fakeClaude(mode))
	}

	os.Exit(m.Run())
}

// fakeClaude answers like Claude Code: initialize, then one turn whose shape
// depends on mode. It records its arguments and first prompt in its cwd.
func fakeClaude(mode string) int {
	_ = os.WriteFile("argv.txt", []byte(strings.Join(os.Args[1:], " ")), 0o600) //nolint:gosec // fixed name in the cwd
	//nolint:gosec // a fixed name in the cwd
	_ = os.WriteFile("token.txt", []byte(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")), 0o600)

	var gateway []string

	for _, name := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_PROMPT_CACHE_TTL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_AUTO_COMPACT_WINDOW"} {
		gateway = append(gateway, name+"="+os.Getenv(name))
	}

	//nolint:gosec // a fixed name in the cwd
	_ = os.WriteFile("gateway.txt", []byte(strings.Join(gateway, "\n")), 0o600)

	out := json.NewEncoder(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 4<<20)

	exit := 0

	for in.Scan() {
		var msg struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Response struct {
				RequestID string `json:"request_id"`
			} `json:"response"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}

		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}

		switch {
		case msg.Type == "control_request" && msg.Request.Subtype == "initialize":
			emit(out, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success"}})
		case msg.Type == "control_request" && msg.Request.Subtype == "interrupt":
			_ = os.WriteFile("interrupted.txt", []byte("yes"), 0o600)
			emit(out, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true,
				"result": "interrupted"})
		case msg.Type == "user":
			_ = os.WriteFile("prompt.txt", []byte(msg.Message.Content), 0o600)

			status := "connected"
			if mode == "toolsdown" {
				status = "failed"
			}

			emit(out, map[string]any{"type": "system", "subtype": "init", "session_id": "sess_fake",
				"mcp_servers": []map[string]any{{"name": "hypomnema", "status": status}}})

			if mode == "toolsdown" {
				continue
			}

			if mode == "die" {
				return 3
			}

			emit(out, map[string]any{
				"type": "control_request", "request_id": "perm_1",
				"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Write", "input": map[string]any{"x": 1}},
			})
		case msg.Type == "control_response" && msg.Response.RequestID == "perm_1":
			if mode == "stale-queue" || mode == "stale-tool" {
				if mode == "stale-queue" {
					emit(out, map[string]any{"type": "result", "subtype": "success", "result": "waiting", "queued_turn_count": 1})
					emit(out, map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running"})
				} else {
					emit(out, map[string]any{"type": "system", "subtype": "background_tasks_changed",
						"tasks": []map[string]string{{"task_type": "local_bash"}}})
					emit(out, map[string]any{"type": "result", "subtype": "success", "result": "waiting"})
					emit(out, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
						{"type": "tool_use", "id": "stale", "name": "Bash"},
					}}})
					emit(out, map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []any{}})
				}
				emit(out, map[string]any{"type": "system", "subtype": "session_state_changed", "state": "idle"})
				continue
			}
			if mode == "idle-after-background" || mode == "stuck-after-eof" {
				emit(out, map[string]any{"type": "system", "subtype": "background_tasks_changed",
					"tasks": []map[string]string{{"task_id": "bg", "task_type": "local_bash"}}})
				emit(out, map[string]any{"type": "result", "subtype": "success", "result": "waiting"})
				emit(out, map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []any{}})
				emit(out, map[string]any{"type": "system", "subtype": "session_state_changed", "state": "idle"})
				continue
			}

			if mode == "background" || mode == "agent" || mode == "workflow" || mode == "queued" || mode == "running" ||
				mode == "tool-in-flight" {
				emitInterimResult(out, mode)
				continue
			}

			if strings.HasPrefix(mode, "recover") {
				emitFailedFinish(out, mode)
				continue
			}

			emit(out, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
				{"type": "text", "text": "Adding the button."},
				{"type": "tool_use", "id": "call_1", "name": "Write", "input": map[string]any{"file_path": "login.tsx"}},
			}}})
			emit(out, map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "call_1", "content": "wrote login.tsx"},
			}}})

			if mode == "error" {
				emit(out, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true,
					"result": "boom"})
				exit = 1

				continue
			}
			if mode == "limited" {
				emit(out, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true,
					"api_error_status": 429, "result": "API error"})
				exit = 1

				continue
			}

			if mode != "nofinish" {
				_ = bridge.Finish(bridgeDir(), bridge.Final{
					Summary:  "added the button",
					Evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
				})
			}

			emit(out, map[string]any{"type": "result", "subtype": "success", "is_error": false,
				"result": "did it; https://github.com/kl3inIT/second-bite/pull/7",
				"usage":  map[string]any{"input_tokens": 1200, "output_tokens": 300, "cache_read_input_tokens": 9000}})
		case msg.Type == "control_response" && msg.Response.RequestID == "perm_after":
			emit(out, map[string]any{"type": "control_request", "request_id": "perm_finish",
				"request": map[string]any{"subtype": "can_use_tool", "tool_name": "mcp__hypomnema__finish"}})
		case msg.Type == "control_response" && msg.Response.RequestID == "perm_finish":
			_ = bridge.Finish(bridgeDir(), bridge.Final{Summary: "finished after Bash"})
			emit(out, map[string]any{"type": "result", "subtype": "success", "result": "done"})
		}
	}

	if mode == "stuck-after-eof" {
		_ = os.WriteFile("input-closed.txt", []byte("yes"), 0o600)
		time.Sleep(time.Minute)
	}
	return exit
}

func emitInterimResult(out *json.Encoder, mode string) {
	queued := mode == "queued"
	result := map[string]any{"type": "result", "subtype": "success", "result": "waiting"}
	// A planner's Agent and a workflow run in the background as Bash does, with no queued turn.
	taskType := map[string]string{"agent": "local_agent", "workflow": "local_workflow"}[mode]
	if taskType == "" {
		taskType = "local_bash"
	}
	if queued {
		result["queued_turn_count"] = 1
	} else {
		emit(out, map[string]any{"type": "system", "subtype": "background_tasks_changed",
			"tasks": []map[string]string{{"task_id": "bg", "task_type": taskType}}})
	}
	emit(out, result)
	if mode == "tool-in-flight" {
		emit(out, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
			{"type": "tool_use", "id": "pending", "name": "Bash", "input": map[string]any{"command": "true"}},
		}}})
	}
	emit(out, map[string]any{"type": "system", "subtype": "session_state_changed", "state": "idle"})
	time.Sleep(500 * time.Millisecond)
	if !queued {
		emit(out, map[string]any{"type": "system", "subtype": "task_notification", "task_id": "bg", "status": "stopped"})
		emit(out, map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []any{}})
	}
	if mode == "running" {
		emit(out, map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running"})
	}
	if mode == "running" || mode == "tool-in-flight" || queued {
		time.Sleep(settleWait + 500*time.Millisecond)
	}
	emit(out, map[string]any{"type": "control_request", "request_id": "perm_after",
		"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash"}})
}

func emitFailedFinish(out *json.Encoder, mode string) {
	input := map[string]any{
		"summary": "recovered finish", "evidence": []nodeproto.Evidence{{Kind: "pull_request", Ref: "https://example.test/pull/7"}},
	}
	callID, result := "final", "Tool permission request failed: AbortError: Stream closed"
	switch mode {
	case "recover-denied":
		result = "Tool permission request failed: denied"
	case "recover-wrong-id":
		callID = "different-tool"
	case "recover-invalid":
		input["summary"] = " "
	case "recover-recorded":
		_ = bridge.Finish(bridgeDir(), bridge.Final{Summary: "already recorded"})
	}
	emit(out, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": "final", "name": "mcp__hypomnema__finish", "input": input},
	}}})
	emit(out, map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_result", "tool_use_id": callID, "is_error": true, "content": result},
	}}})
	emit(out, map[string]any{"type": "result", "subtype": "success", "result": "could not call finish"})
}

// bridgeDir is the bridge directory named in the worker's --mcp-config, as
// the real tools server gets it.
func bridgeDir() string {
	for i, a := range os.Args {
		if a != "--mcp-config" || i+1 >= len(os.Args) {
			continue
		}

		raw, err := os.ReadFile(os.Args[i+1]) //nolint:gosec // the config the runner names
		if err != nil {
			return ""
		}

		var cfg struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}

		if json.Unmarshal(raw, &cfg) != nil {
			return ""
		}

		if args := cfg.MCPServers["hypomnema"].Args; len(args) == 3 {
			return args[2]
		}
	}

	return ""
}

// plane plays the control plane for the transcript of a worker.
type plane struct {
	mu      sync.Mutex
	batches []nodeproto.Items
}

func (p *plane) send(b nodeproto.Items) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.batches = append(p.batches, b)

	return true
}

func (p *plane) sent() []nodeproto.Items {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]nodeproto.Items(nil), p.batches...)
}

// reporter records what the Supervisor reports.
type reporter struct {
	mu       sync.Mutex
	id       string
	started  []string
	finished []nodeproto.Finished
	exited   int
	stopped  int
	done     chan struct{}
	once     sync.Once
}

func newReporter(id string) *reporter { return &reporter{id: id, done: make(chan struct{})} }

func (r *reporter) end() { r.once.Do(func() { close(r.done) }) }

func (r *reporter) ID() string { return r.id }

func (r *reporter) Started(_ context.Context, processRef, session string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.started = append(r.started, processRef+" "+session)

	return nil
}

func (r *reporter) Ask(context.Context, nodeproto.Ask) (string, error) { return "rep_q", nil }

func (r *reporter) AnswerReceived(context.Context, string) error { return nil }

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
	sup      *runner.Supervisor
	rt       *ClaudeRuntime
	worktree string
}

func newRig(t *testing.T, mode string) *rig {
	t.Helper()

	home := t.TempDir()

	exe, err := os.Executable()
	require.NoError(t, err)

	return &rig{
		t: t, worktree: filepath.Join(t.TempDir(), "wt"),
		sup: &runner.Supervisor{Home: home, Exe: exe, Logger: slog.New(slog.DiscardHandler)},
		rt: &ClaudeRuntime{
			Home: home, Exe: exe, Claude: exe, URL: "https://cp.example/",
			Env: []string{"FAKE_CLAUDE=" + mode},
		},
	}
}

func (g *rig) dispatch(id string) statedb.Dispatch {
	return statedb.Dispatch{
		ID: id, State: statedb.StateAccepted, Epoch: 1,
		Offer: nodeproto.Offer{
			DispatchID: id, TaskID: "tsk_1", RunID: "run_1", Profile: "implementer", Spec: "add the Google login button",
			Worktree: g.worktree, Runtime: "claude", AccountRef: "gateway", ModelKey: "hyp_gateway_v1_dispatch",
			Model: "opus", Effort: "xhigh",
		},
		Accepted: &nodeproto.Accepted{Epoch: 1},
	}
}

func (g *rig) file(name string) string {
	g.t.Helper()

	raw, err := os.ReadFile(filepath.Join(g.worktree, name)) //nolint:gosec // the test's own temporary directory
	require.NoError(g.t, err)

	return string(raw)
}

// Scenario: one turn that succeeds. The worker gets the prompt, its token and
// the model; its permission prompt is allowed; the session id and the
// result's summary and usage are reported.
func TestWorkerRunsToSuccess(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	p := &plane{}
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("dsp_1"), r))
	r.wait(t)

	// The transcript of the worker just run is ready for the control plane.
	(&transcript.Forwarder{
		Home: g.rt.Home, Send: p.send, Logger: slog.New(slog.DiscardHandler),
		Decoders: map[string]func() transcript.Decoder{Format: NewDecoder},
	}).Step(t.Context())

	format, err := os.ReadFile(filepath.Join(g.rt.Home, "run", "dsp_1", "format"))
	require.NoError(t, err)
	assert.Equal(t, "claude", string(format), "the journal says how it is to be read")

	require.Len(t, p.sent(), 1)
	sent := p.sent()[0]
	assert.Equal(t, "dsp_1", sent.DispatchID)
	require.Len(t, sent.Items, 3)
	assert.Equal(t, nodeproto.Item{Seq: 1, Kind: nodeproto.ItemMessage, Text: "Adding the button."}, sent.Items[0])
	assert.Equal(t, "Write", sent.Items[1].Tool)
	assert.Equal(t, nodeproto.Item{Seq: 3, Kind: nodeproto.ItemToolResult, CallID: "call_1", Text: "wrote login.tsx"},
		sent.Items[2])

	require.Len(t, r.finished, 1)
	f := r.finished[0]
	assert.Empty(t, f.Class)
	assert.Equal(t, "added the button", f.Summary, "the outcome is the finish tool's report")
	assert.Equal(t, []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}}, f.Evidence)
	require.NotNil(t, f.Usage)
	assert.Equal(t, int64(9000), f.Usage.CacheRead)
	require.NotNil(t, f.Exit.Code)
	assert.Equal(t, 0, *f.Exit.Code)

	require.Len(t, r.started, 1)
	assert.True(t, strings.HasSuffix(r.started[0], " sess_fake"))

	assert.Contains(t, g.file("prompt.txt"), "add the Google login button")
	assert.Contains(t, g.file("argv.txt"), "--permission-prompt-tool stdio")
	assert.Contains(t, g.file("argv.txt"), "--model opus")
	assert.Contains(t, g.file("argv.txt"), "--effort xhigh")
	assert.Contains(t, g.file("argv.txt"), "--disallowedTools AskUserQuestion,Task,Agent,WebSearch,WebFetch",
		"a worker cannot ask outside ask or start subagents, and searches and reads pages through the gateway")
	assert.Contains(t, g.file("gateway.txt"), "ANTHROPIC_AUTH_TOKEN=hyp_gateway_v1_dispatch")
	assert.Empty(t, g.file("token.txt"), "no subscription token on the node")

	_, err = os.Stat(filepath.Join(filepath.Join(g.rt.Home, "run", "dsp_1"), shim.SpecFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "the spec with the token is gone once the worker runs")
}

// Scenario: the turn fails. It is a worker error with the result's text.
func TestFailedTurnIsAWorkerError(t *testing.T) {
	t.Parallel()

	g := newRig(t, "error")
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("dsp_1"), r))
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.Equal(t, "stopped without finish: boom", r.finished[0].Summary)
}

func TestAPI429IsRateLimitedEvenWithoutAResetEvent(t *testing.T) {
	t.Parallel()
	g := newRig(t, "limited")
	reporter := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("dsp_1"), reporter))
	reporter.wait(t)
	require.Len(t, reporter.finished, 1)
	assert.Equal(t, "rate_limited", reporter.finished[0].Class)
	assert.Nil(t, reporter.finished[0].ResetsAt)
}

// Scenario: Claude Code says at start that the tools server failed (issue
// #492). The worker could neither finish nor use the gateway's tools, so its
// turn is stopped at once and fails, saying why.
func TestATurnWhoseToolsServerFailedFails(t *testing.T) {
	t.Parallel()

	g := newRig(t, "toolsdown")
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("dsp_1"), r))
	r.wait(t)

	assert.Equal(t, "yes", g.file("interrupted.txt"), "the turn is stopped")
	require.Len(t, r.finished, 1)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.Equal(t, "the worker's tools server (hypomnema) did not start: its status was failed",
		r.finished[0].Summary)
}

// Scenario: the turn ends well but the worker never called finish. It fails
// (Orca: "Worker stopped without reporting").
func TestTurnWithoutFinishFails(t *testing.T) {
	t.Parallel()

	g := newRig(t, "nofinish")
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("dsp_1"), r))
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.True(t, strings.HasPrefix(r.finished[0].Summary, "stopped without finish"))
}

func TestInterimResultKeepsPermissionStreamOpen(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"background", "agent", "workflow", "queued", "running", "tool-in-flight"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			g := newRig(t, mode)
			if mode == "tool-in-flight" {
				g.rt.closeGrace = time.Second
			}
			r := newReporter("dsp_1")
			require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch(r.ID()), r))
			t.Cleanup(func() { g.sup.Release(r.ID()) })
			r.wait(t)

			require.Len(t, r.finished, 1)
			assert.Empty(t, r.finished[0].Class)
			assert.Equal(t, "finished after Bash", r.finished[0].Summary)
		})
	}
}

func TestBackgroundSettlesWithoutAnotherResult(t *testing.T) {
	t.Parallel()

	g := newRig(t, "idle-after-background")
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch(r.ID()), r))
	t.Cleanup(func() { g.sup.Release(r.ID()) })
	select {
	case <-r.done:
	case <-time.After(6 * time.Second):
		t.Fatal("the worker stayed open after background Bash and the final result")
	}
	require.Len(t, r.finished, 1)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.Equal(t, "stopped without finish: waiting", r.finished[0].Summary)
}

func TestIdleSupersedesStaleWork(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"stale-queue", "stale-tool"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			g := newRig(t, mode)
			r := newReporter("dsp_1")
			require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch(r.ID()), r))
			t.Cleanup(func() { g.sup.Release(r.ID()) })
			select {
			case <-r.done:
			case <-time.After(8 * time.Second):
				t.Fatal("stale work kept stdin open after idle")
			}
			require.Len(t, r.finished, 1)
			assert.Equal(t, "stopped without finish: waiting", r.finished[0].Summary)
		})
	}
}

func TestBackgroundContinuationThatIgnoresEOFIsKilled(t *testing.T) {
	t.Parallel()

	g := newRig(t, "stuck-after-eof")
	g.rt.closeGrace = 500 * time.Millisecond
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch(r.ID()), r))
	t.Cleanup(func() { g.sup.Release(r.ID()) })
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Zero(t, r.stopped)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.Equal(t, "Claude Code did not exit after its input was closed", r.finished[0].Summary)
	assert.Equal(t, "yes", g.file("input-closed.txt"))
	dir := filepath.Join(g.sup.Home, "run", r.ID())
	exit, done, err := shim.ReadExit(dir)
	require.NoError(t, err)
	require.True(t, done, "the transcript can deliver the journal's final lines")
	if runtime.GOOS == "windows" {
		assert.Equal(t, "exited", r.finished[0].Exit.Kind)
		assert.Empty(t, exit.Signal)
	} else {
		assert.Equal(t, -1, exit.Code)
		assert.Equal(t, "signaled", r.finished[0].Exit.Kind)
		assert.NotEmpty(t, r.finished[0].Exit.Signal)
		assert.Nil(t, r.finished[0].Exit.Code)
	}
	pid, err := shim.PID(dir)
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return !shim.Alive(pid) }, 3*time.Second, 20*time.Millisecond)
}

func TestRecoversOnlyAnExplicitFinishWithAClosedPermissionStream(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		mode, class, summary string
		evidence             []nodeproto.Evidence
	}{
		{mode: "recover", summary: "recovered finish",
			evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "https://example.test/pull/7"}}},
		{mode: "recover-recorded", summary: "already recorded"},
		{mode: "recover-denied", class: "worker_error", summary: "stopped without finish: could not call finish"},
		{mode: "recover-wrong-id", class: "worker_error", summary: "stopped without finish: could not call finish"},
		{mode: "recover-invalid", class: "worker_error", summary: "stopped without finish: could not call finish"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Parallel()
			g := newRig(t, test.mode)
			r := newReporter("dsp_1")
			require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch(r.ID()), r))
			r.wait(t)

			require.Len(t, r.finished, 1)
			assert.Equal(t, test.class, r.finished[0].Class)
			assert.Equal(t, test.summary, r.finished[0].Summary)
			assert.Equal(t, test.evidence, r.finished[0].Evidence)
		})
	}
}

// Scenario: the process dies after it started, with no result. The outcome
// is unknown, so it is reported as exited, not finished.
func TestDeathWithoutAResultIsAnExit(t *testing.T) {
	t.Parallel()

	g := newRig(t, "die")
	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("dsp_1"), r))
	r.wait(t)

	assert.Equal(t, 1, r.exited)
	assert.Empty(t, r.finished)
}

// Scenario: an offer that names an account, as a control plane from before
// issue #381 sent for a Claude account a node held or whose token it kept.
// The start fails at once and says the worker runs through the gateway.
func TestAnOfferUnderAnAccountFailsTheStart(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	d := g.dispatch("dsp_1")
	d.Offer.AccountRef, d.Offer.ModelKey = "acc_other", ""

	require.ErrorContains(t, g.sup.Start(t.Context(), g.rt, d, newReporter("dsp_1")),
		`the offer names account "acc_other": a Claude Code worker runs only through the model gateway`)
}

func emit(out *json.Encoder, v map[string]any) {
	if err := out.Encode(v); err != nil {
		os.Exit(4)
	}
}

// Scenario: the gateway serves the worker's model (issue #381). The worker
// calls the control plane with its Dispatch's key, asks for the one-hour
// cache, names no other model, and gets no account's token.
func TestWorkerReachesClaudeThroughTheGateway(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	g.rt.URL = "https://cp.example/"
	d := g.dispatch("dsp_1")
	d.Offer.AccountRef, d.Offer.ModelKey, d.Offer.Model = "gateway", "hyp_gateway_v1_dispatch", "claude-sonnet-5-5"
	d.Offer.ModelContextWindow = 200000

	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, d, r))
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Empty(t, r.finished[0].Class)
	assert.Empty(t, g.file("token.txt"), "no account's token")
	assert.Equal(t, "ANTHROPIC_BASE_URL=https://cp.example\nANTHROPIC_AUTH_TOKEN=hyp_gateway_v1_dispatch\n"+
		"CLAUDE_CODE_PROMPT_CACHE_TTL=1h\nANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-5-5\n"+
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-sonnet-5-5\nCLAUDE_CODE_AUTO_COMPACT_WINDOW=200000", g.file("gateway.txt"))

	keyless := g.dispatch("dsp_2")
	keyless.Offer.ModelKey = ""
	require.ErrorContains(t, g.sup.Start(t.Context(), g.rt, keyless, newReporter("dsp_2")),
		"the offer carries no key of the model gateway")
}

// Scenario: the offer gives the worker a skill the node has kept. Claude Code
// is given a plugin folder of the Dispatch's own that holds it (issue #21).
func TestTheWorkersSkillsAreAPluginOfItsOwn(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	digest := strings.Repeat("ab", 32)
	kept := filepath.Join(g.rt.Home, "skills", digest)
	require.NoError(t, os.MkdirAll(kept, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kept, "SKILL.md"), []byte("---\nname: review-go\n---\n"), 0o600))

	d := g.dispatch("dsp_1")
	d.Offer.Skills = []nodeproto.Skill{{Name: "review-go", Revision: 2, Digest: digest}}

	r := newReporter("dsp_1")
	require.NoError(t, g.sup.Start(t.Context(), g.rt, d, r))
	r.wait(t)

	plugin := filepath.Join(filepath.Join(g.rt.Home, "run", "dsp_1"), "plugin")
	assert.Contains(t, g.file("argv.txt"), "--plugin-dir "+plugin)
	assert.FileExists(t, filepath.Join(plugin, "skills", "review-go", "SKILL.md"))
	assert.FileExists(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"))
}

// Scenario: a worker's environment. Claude Code's own memory is off, so the
// system prompt it caches has no path of a worktree (issue #21).
func TestClaudeCodesOwnMemoryIsOff(t *testing.T) {
	t.Parallel()

	assert.Contains(t, newRig(t, "success").rt.env("dir", nil), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
}

// Scenario: a worker resumes its session after a commit. Claude Code adds no git instructions or status snapshot, so
// the cached prefix stays the same across follow-ups (issue #1019).
func TestClaudeCodesGitSnapshotIsOff(t *testing.T) {
	t.Parallel()

	assert.Contains(t, newRig(t, "success").rt.env("dir", nil), "CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1")
}
