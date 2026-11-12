package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The test binary plays three parts: the tests, the shim (`shim --dir D`),
// and a fake claude (FAKE_CLAUDE set) that speaks stream-JSON.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shim" && os.Args[2] == "--dir" {
		if err := shim.Main(context.Background(), os.Args[3]); err != nil {
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
			emit(out, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true})
			exit = 1
		case msg.Type == "user":
			_ = os.WriteFile("prompt.txt", []byte(msg.Message.Content), 0o600)
			emit(out, map[string]any{"type": "system", "subtype": "init", "session_id": "sess_fake"})

			switch mode {
			case "die":
				return 3
			case "hang":
				continue
			}

			emit(out, map[string]any{
				"type": "control_request", "request_id": "perm_1",
				"request": map[string]any{"subtype": "can_use_tool", "tool_name": "Write", "input": map[string]any{"x": 1}},
			})
		case msg.Type == "control_response" && msg.Response.RequestID == "perm_1":
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

			dir := bridgeDir()

			if mode == "ask" {
				seq, err := bridge.Ask(dir, nodeproto.Ask{
					Text: "goth or hand-written?", Options: []nodeproto.Option{{Label: "goth"}, {Label: "hand-written"}},
					To: "owner",
				})
				if err != nil {
					return 5
				}

				for {
					answer, ok, _ := bridge.Answered(dir, seq)
					if ok {
						_ = os.WriteFile("answer.txt", []byte(answer), 0o600)

						break
					}

					time.Sleep(20 * time.Millisecond)
				}
			}

			if mode != "nofinish" {
				_ = bridge.Finish(dir, bridge.Final{
					Summary:  "added the button",
					Evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
				})
			}

			emit(out, map[string]any{"type": "result", "subtype": "success", "is_error": false,
				"result": "did it; https://github.com/kl3inIT/second-bite/pull/7",
				"usage":  map[string]any{"input_tokens": 1200, "output_tokens": 300, "cache_read_input_tokens": 9000}})
		}
	}

	return exit
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

// reporter records what the runner reports.
type reporter struct {
	mu       sync.Mutex
	id       string
	started  []string
	asks     []nodeproto.Ask
	received []string
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

func (r *reporter) Progress(context.Context) error { return nil }

func (r *reporter) Ask(_ context.Context, a nodeproto.Ask) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.asks = append(r.asks, a)

	return "rep_q", nil
}

func (r *reporter) AnswerReceived(_ context.Context, questionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.received = append(r.received, questionID)

	return nil
}

func (r *reporter) asked() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.asks)
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
	runner   *Runner
	worktree string
}

func newRig(t *testing.T, mode string) *rig {
	t.Helper()

	home := t.TempDir()
	tokenFile := filepath.Join(home, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("sk-ant-oat01-test\n"), 0o600))

	exe, err := os.Executable()
	require.NoError(t, err)

	return &rig{
		t: t, worktree: filepath.Join(t.TempDir(), "wt"),
		runner: &Runner{
			Home: home, Exe: exe, Claude: exe, Accounts: map[string]Account{"acc_1": {TokenFile: tokenFile}},
			Env: []string{"FAKE_CLAUDE=" + mode}, Logger: slog.New(slog.DiscardHandler),
		},
	}
}

func (g *rig) dispatch(id string) statedb.Dispatch {
	return statedb.Dispatch{
		ID: id, State: statedb.StateAccepted, Epoch: 1,
		Offer: nodeproto.Offer{
			DispatchID: id, TaskID: "tsk_1", RunID: "run_1", Profile: "implementer", Spec: "add the Google login button",
			Worktree: g.worktree, Runtime: "claude", AccountRef: "acc_1",
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
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch("dsp_1"), r))
	r.wait(t)

	// The transcript of the worker just run is ready for the control plane.
	(&transcript.Forwarder{
		Home: g.runner.Home, Send: p.send, Logger: slog.New(slog.DiscardHandler),
		Decoders: map[string]func() transcript.Decoder{Format: NewDecoder},
	}).Step(t.Context())

	format, err := os.ReadFile(filepath.Join(g.runner.Home, "run", "dsp_1", "format"))
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
	assert.Contains(t, g.file("argv.txt"), "--disallowedTools AskUserQuestion,Task,Agent",
		"a worker cannot ask outside ask or start subagents")
	assert.Equal(t, "sk-ant-oat01-test", g.file("token.txt"))

	_, err = os.Stat(filepath.Join(g.runner.dir("dsp_1"), shim.SpecFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "the spec with the token is gone once the worker runs")
}

// Scenario: the turn fails. It is a worker error with the result's text.
func TestFailedTurnIsAWorkerError(t *testing.T) {
	t.Parallel()

	g := newRig(t, "error")
	r := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch("dsp_1"), r))
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.Equal(t, "stopped without finish: boom", r.finished[0].Summary)
}

// Scenario: the turn ends well but the worker never called finish. It fails
// (Orca: "Worker stopped without reporting").
func TestTurnWithoutFinishFails(t *testing.T) {
	t.Parallel()

	g := newRig(t, "nofinish")
	r := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch("dsp_1"), r))
	r.wait(t)

	require.Len(t, r.finished, 1)
	assert.Equal(t, "worker_error", r.finished[0].Class)
	assert.True(t, strings.HasPrefix(r.finished[0].Summary, "stopped without finish"))
}

// Scenario: the worker asks the owner through its tool. The question is
// reported, the control plane's id is recorded, the answer reaches the
// waiting tool with the option's label, and it is acknowledged.
func TestAskIsRelayedAndAnswered(t *testing.T) {
	t.Parallel()

	g := newRig(t, "ask")
	g.runner.URL = "https://hypomnema.example"

	d := g.dispatch("dsp_1")
	d.Offer.AgentKey = "the-dispatch's-key"

	r := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), d, r))

	require.Eventually(t, func() bool { return r.asked() == 1 }, 20*time.Second, 20*time.Millisecond)
	assert.Equal(t, "goth or hand-written?", r.asks[0].Text)

	// The tools server finds the control plane's agent API and the Dispatch's key where it finds the rest: with
	// them it reaches the shared browser (issue #212).
	agent, ok, err := bridge.AgentOf(bridge.Dir(g.runner.dir("dsp_1")))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, bridge.Agent{URL: "https://hypomnema.example", Key: "the-dispatch's-key"}, agent)

	g.runner.Asked("dsp_1", "rep_q", "q_1")

	second := 1
	g.runner.Answer("dsp_1", nodeproto.Question{
		ID: "q_1", Options: []nodeproto.Option{{Label: "goth"}, {Label: "hand-written"}},
		Answer: &nodeproto.Answer{Option: &second, Text: "keep it small"},
	})
	r.wait(t)

	assert.Equal(t, "hand-written keep it small", g.file("answer.txt"))
	assert.Equal(t, []string{"q_1"}, r.received)
	require.Len(t, r.finished, 1)
	assert.Empty(t, r.finished[0].Class)
	assert.Equal(t, 1, r.asked(), "a question is asked once")
}

// Scenario: the process dies after it started, with no result. The outcome
// is unknown, so it is reported as exited, not finished.
func TestDeathWithoutAResultIsAnExit(t *testing.T) {
	t.Parallel()

	g := newRig(t, "die")
	r := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch("dsp_1"), r))
	r.wait(t)

	assert.Equal(t, 1, r.exited)
	assert.Empty(t, r.finished)
}

// Scenario: the daemon restarts while the worker waits. A new runner adopts
// it from its journal, and a stop through the new runner interrupts it and
// is confirmed.
func TestAdoptedWorkerCanBeStopped(t *testing.T) {
	t.Parallel()

	g := newRig(t, "hang")
	first := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch("dsp_1"), first))

	require.Eventually(t, func() bool {
		first.mu.Lock()
		defer first.mu.Unlock()

		return len(first.started) == 1
	}, 20*time.Second, 20*time.Millisecond)

	// The old daemon goes away without touching the worker.
	g.runner.drop("dsp_1").cancel()

	again := &Runner{
		Home: g.runner.Home, Exe: g.runner.Exe, Claude: g.runner.Claude, Accounts: g.runner.Accounts,
		Logger: slog.New(slog.DiscardHandler),
	}
	second := newReporter("dsp_1")
	require.NoError(t, again.Adopt(t.Context(), g.dispatch("dsp_1"), second))

	again.Stop("dsp_1")
	second.wait(t)

	assert.Equal(t, 1, second.stopped)
	assert.Empty(t, second.started, "the adopted worker was already reported started")
}

// Scenario: an account the node has no credentials for. The start fails at once.
func TestUnknownAccountFailsTheStart(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	d := g.dispatch("dsp_1")
	d.Offer.AccountRef = "acc_other"

	require.ErrorContains(t, g.runner.Start(t.Context(), d, newReporter("dsp_1")), "no credentials for account acc_other")
}

// Scenario: the first turn after a Gate and after an interruption. The
// prompt carries the decision and the recovery note.
func TestPromptCarriesTheGateAndTheRecoveryNote(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	d := g.dispatch("dsp_1")
	d.Offer.Repo, d.Offer.Base = "kl3inIT/second-bite", "main"

	first := 0
	d.Accepted = &nodeproto.Accepted{Epoch: 1, Recovery: true, Gate: &nodeproto.Question{
		Text: "PR #7 is green. Merge it?", Options: []nodeproto.Option{{Label: "merge"}, {Label: "change first"}},
		Answer: &nodeproto.Answer{Option: &first},
	}}

	p := Prompt(d)
	assert.Contains(t, p, "branch hyp/tsk_1")
	assert.Contains(t, p, "gh pr create --base main --head hyp/tsk_1",
		"gh does not find the branch in a worktree of the mirror without --head")
	assert.Contains(t, p, "the pull request as its URL", "a URL names the host; owner/repo#N does not")

	d.Offer.RepoHost, d.Offer.RepoKind = "gitlab.company.example", "gitlab"
	assert.Contains(t, Prompt(d), "glab mr create --target-branch main --source-branch hyp/tsk_1 --yes",
		"a worker on GitLab opens its request with GitLab's own tool")
	d.Offer.RepoHost, d.Offer.RepoKind = "", ""
	assert.Contains(t, p, "outcome is unknown")
	assert.Contains(t, p, "DECISION GATE RESOLVED. You asked: PR #7 is green. Merge it?\nThe answer: merge")
	assert.NotContains(t, p, "For this kind of task", "no instruction, no section")

	d.Offer.Profile, d.Offer.Instruction = "reviewer", "Report problems; change nothing.\n"
	p = Prompt(d)
	assert.Contains(t, p, "Task (reviewer):")
	assert.Contains(t, p, "Rules:")
	assert.Greater(t, strings.Index(p, "For this kind of task:\nReport problems; change nothing."),
		strings.Index(p, "Rules:"), "the profile's instruction follows the rules")
}

func emit(out *json.Encoder, v map[string]any) {
	if err := out.Encode(v); err != nil {
		os.Exit(4)
	}
}

// Scenario: the offer carries the account's token (issue #150). The worker
// runs under it on a node that holds no token of that account; its
// configuration directory is still the account's own.
func TestWorkerRunsUnderTheOffersToken(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	d := g.dispatch("dsp_1")
	d.Offer.AccountRef, d.Offer.AccountToken = "acc_kept", "sk-ant-oat01-from-the-control-plane"

	r := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), d, r))
	r.wait(t)

	assert.Equal(t, "sk-ant-oat01-from-the-control-plane", g.file("token.txt"))
	require.Len(t, r.finished, 1)
	assert.Empty(t, r.finished[0].Class)

	_, err := os.Stat(filepath.Join(g.runner.Home, "accounts", "acc_kept", "claude"))
	require.NoError(t, err)

	missing := g.dispatch("dsp_2")
	missing.Offer.AccountRef = "acc_nowhere"
	require.ErrorContains(t, g.runner.Start(t.Context(), missing, newReporter("dsp_2")),
		"the node has no credentials for account acc_nowhere")
}

// Scenario: the offer gives the worker a skill the node has kept. Claude Code
// is given a plugin folder of the Dispatch's own that holds it (issue #21).
func TestTheWorkersSkillsAreAPluginOfItsOwn(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	digest := strings.Repeat("ab", 32)
	kept := filepath.Join(g.runner.Home, "skills", digest)
	require.NoError(t, os.MkdirAll(kept, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kept, "SKILL.md"), []byte("---\nname: review-go\n---\n"), 0o600))

	d := g.dispatch("dsp_1")
	d.Offer.Skills = []nodeproto.Skill{{Name: "review-go", Revision: 2, Digest: digest}}

	r := newReporter("dsp_1")
	require.NoError(t, g.runner.Start(t.Context(), d, r))
	r.wait(t)

	plugin := filepath.Join(g.runner.dir("dsp_1"), "plugin")
	assert.Contains(t, g.file("argv.txt"), "--plugin-dir "+plugin)
	assert.FileExists(t, filepath.Join(plugin, "skills", "review-go", "SKILL.md"))
	assert.FileExists(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"))
}
