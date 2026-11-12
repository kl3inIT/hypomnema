package codex

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The test binary plays three parts: the tests, the shim (`shim --dir D`),
// and a fake codex (FAKE_CODEX set) that prints what `codex exec --json` does.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shim" && os.Args[2] == "--dir" {
		if err := shim.Main(context.Background(), os.Args[3]); err != nil {
			os.Exit(2)
		}

		os.Exit(0)
	}

	if mode := os.Getenv("FAKE_CODEX"); mode != "" {
		os.Exit(fakeCodex(mode))
	}

	os.Exit(m.Run())
}

// fakeCodex is one turn of `codex exec --json -`: it reads the prompt to the
// end of its input, records what it was given in its cwd, and prints events.
func fakeCodex(mode string) int {
	prompt, _ := io.ReadAll(os.Stdin)

	//nolint:gosec // fixed names in the cwd
	_ = os.WriteFile("argv.txt", []byte(strings.Join(os.Args[1:], "\n")), 0o600)
	_ = os.WriteFile("prompt.txt", prompt, 0o600)
	//nolint:gosec // fixed names in the cwd
	_ = os.WriteFile("env.txt", []byte(os.Getenv(keyVariable)+"\n"+os.Getenv("CODEX_HOME")), 0o600)

	out := json.NewEncoder(os.Stdout)

	if mode == "refused" {
		emit(out, map[string]any{"type": "error", "message": "unexpected status 401 Unauthorized"})

		return 1
	}

	emit(out, map[string]any{"type": "thread.started", "thread_id": "0199-thread"})
	emit(out, map[string]any{"type": "turn.started"})
	emit(out, map[string]any{
		"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "Adding the button."},
	})

	switch mode {
	case "hang":
		time.Sleep(time.Minute)

		return 0
	case "limited":
		emit(out, map[string]any{
			"type": "turn.failed", "error": map[string]any{"message": "You've hit your usage limit."},
		})

		return 1
	case "nofinish":
	default:
		_ = bridge.Finish(bridgeDir(), bridge.Final{
			Summary:  "added the button",
			Evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
		})
	}

	emit(out, map[string]any{"type": "turn.completed", "usage": map[string]any{
		"input_tokens": 10200, "cached_input_tokens": 9000, "output_tokens": 300,
	}})

	return 0
}

func emit(out *json.Encoder, v map[string]any) {
	if err := out.Encode(v); err != nil {
		os.Exit(4)
	}
}

// bridgeDir is the bridge directory named in the tools server's arguments,
// as the real tools server gets it.
func bridgeDir() string {
	for _, a := range os.Args {
		raw, ok := strings.CutPrefix(a, "mcp_servers.hypomnema.args=")
		if !ok {
			continue
		}

		var args []string
		if json.Unmarshal([]byte(raw), &args) == nil && len(args) == 3 {
			return args[2]
		}
	}

	return ""
}

// reporter records what the runner reports.
type reporter struct {
	mu       sync.Mutex
	started  []string
	finished []nodeproto.Finished
	exited   int
	stopped  int
	done     chan struct{}
	once     sync.Once
}

func newReporter() *reporter { return &reporter{done: make(chan struct{})} }

func (r *reporter) end() { r.once.Do(func() { close(r.done) }) }

func (r *reporter) ID() string { return "dsp_1" }

func (r *reporter) Started(_ context.Context, processRef, session string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.started = append(r.started, strings.SplitN(processRef, ":", 2)[0]+" "+session)

	return nil
}

func (r *reporter) Progress(context.Context) error { return nil }

func (r *reporter) Ask(context.Context, nodeproto.Ask) (string, error) { return "rep_q", nil }

func (r *reporter) AnswerReceived(context.Context, string) error { return nil }

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

	exe, err := os.Executable()
	require.NoError(t, err)

	return &rig{
		t: t, worktree: filepath.Join(t.TempDir(), "wt"),
		runner: &Runner{
			Home: t.TempDir(), Exe: exe, Codex: exe, URL: "https://hypomnema.example/",
			Env: []string{"FAKE_CODEX=" + mode}, Logger: slog.New(slog.DiscardHandler),
		},
	}
}

func (g *rig) dispatch(session string) statedb.Dispatch {
	return statedb.Dispatch{
		ID: "dsp_1", State: statedb.StateAccepted, Epoch: 1,
		Offer: nodeproto.Offer{
			DispatchID: "dsp_1", TaskID: "tsk_1", RunID: "run_1", Profile: "implementer",
			Spec: "add the Google login button", ConversationID: "cnv_1", Worktree: g.worktree, Runtime: "codex",
			AccountRef: "gateway", Model: "gpt-sol", Effort: "max", ModelKey: "hyp_gateway_v1_test",
			CLISessionRef: session,
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

// Scenario: a Codex worker's turn that succeeds. The CLI is pointed at the
// control plane's gateway with the Dispatch's key in its environment, not on
// its command line; it keeps its state in the Conversation's own directory;
// it gets the prompt on its input and the tools server; its thread id is
// reported as the session, and finish's summary with the usage as the outcome.
func TestACodexWorkerRunsThroughTheGateway(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	r := newReporter()
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch(""), r))
	r.wait(t)

	assert.Equal(t, []string{"shim 0199-thread"}, r.started)
	require.Len(t, r.finished, 1)

	f := r.finished[0]
	assert.Empty(t, f.Class)
	assert.Equal(t, "added the button", f.Summary)
	assert.Equal(t, []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}}, f.Evidence)
	assert.Equal(t, &nodeproto.Usage{Input: 1200, Output: 300, CacheRead: 9000}, f.Usage)

	format, err := os.ReadFile(filepath.Join(g.runner.Home, "run", "dsp_1", "format"))
	require.NoError(t, err)
	assert.Equal(t, "codex", string(format), "the journal says how it is to be read")

	argv := g.file("argv.txt")
	for _, want := range []string{
		"exec\n--json\n--skip-git-repo-check\n--ignore-user-config\n--dangerously-bypass-approvals-and-sandbox",
		`model_provider="hypomnema"`,
		`model_providers.hypomnema.base_url="https://hypomnema.example/v1"`,
		`model_providers.hypomnema.env_key="HYPOMNEMA_MODEL_KEY"`,
		`model_providers.hypomnema.wire_api="responses"`,
		`model="gpt-sol"`,
		`model_reasoning_effort="xhigh"`,
		`mcp_servers.hypomnema.args=["mcp","--dir",`,
	} {
		assert.Contains(t, argv, want)
	}

	assert.NotContains(t, argv, "hyp_gateway_v1_test", "the key is not on the command line")
	assert.NotContains(t, argv, "\nresume\n")
	assert.True(t, strings.HasSuffix(argv, "\n-"), "the prompt is read from the input")

	env := strings.Split(g.file("env.txt"), "\n")
	assert.Equal(t, "hyp_gateway_v1_test", env[0])
	assert.Equal(t, filepath.Join(g.runner.Home, "conversations", "cnv_1", "codex"), env[1])

	assert.Contains(t, g.file("prompt.txt"), "add the Google login button")
	assert.Contains(t, g.file("prompt.txt"), "call the `finish` tool exactly once")
}

// Scenario: a follow-up Dispatch of a Conversation. The CLI resumes the
// thread it reported, from the Conversation's directory.
func TestAFollowUpResumesTheThread(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	r := newReporter()
	require.NoError(t, g.runner.Start(t.Context(), g.dispatch("0199-thread"), r))
	r.wait(t)

	assert.Contains(t, g.file("argv.txt"), "\nresume\n0199-thread\n-")
	require.Len(t, r.finished, 1)
	assert.Empty(t, r.finished[0].Class)
}

// Scenario: how a turn that does not finish is reported. A key the gateway
// refuses before the thread starts is a failed start; a usage limit is a
// rate limit; a turn that ends without finish is the worker's failure; a
// stopped worker is reported stopped; an offer with no gateway key is refused.
func TestHowACodexTurnEnds(t *testing.T) {
	t.Parallel()

	for mode, want := range map[string]string{
		"refused": "start_failed", "limited": "rate_limited", "nofinish": "worker_error",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			g := newRig(t, mode)
			r := newReporter()
			require.NoError(t, g.runner.Start(t.Context(), g.dispatch(""), r))
			r.wait(t)

			require.Len(t, r.finished, 1)
			assert.Equal(t, want, r.finished[0].Class, r.finished[0].Summary)
		})
	}

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		if runtime.GOOS == "windows" {
			t.Skip("a node runs on Linux: on Windows the shim's worker is in no group to kill")
		}

		g := newRig(t, "hang")
		r := newReporter()
		require.NoError(t, g.runner.Start(t.Context(), g.dispatch(""), r))

		require.Eventually(t, func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()

			return len(r.started) == 1
		}, 10*time.Second, 20*time.Millisecond)

		g.runner.Stop("dsp_1")
		r.wait(t)
		assert.Equal(t, 1, r.stopped)
	})

	t.Run("no key", func(t *testing.T) {
		t.Parallel()

		g := newRig(t, "success")
		d := g.dispatch("")
		d.Offer.ModelKey = ""

		err := g.runner.Start(t.Context(), d, newReporter())
		require.ErrorContains(t, err, "no key of the model gateway")
	})
}

// Scenario: a follow-up Dispatch of a Conversation is given one skill, where
// the first was given another. Codex finds only the one now given in its
// CODEX_HOME's skills, beside its own .system (issue #21).
func TestCodexFindsTheSkillsOfItsDispatch(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	digest := strings.Repeat("cd", 32)
	kept := filepath.Join(g.runner.Home, "skills", digest)
	require.NoError(t, os.MkdirAll(kept, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kept, "SKILL.md"), []byte("---\nname: pdf\n---\n"), 0o600))

	skillsDir := filepath.Join(g.runner.Home, "conversations", "cnv_1", "codex", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(skillsDir, "review-go"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(skillsDir, ".system"), 0o700))

	d := g.dispatch("0199-thread")
	d.Offer.Skills = []nodeproto.Skill{{Name: "pdf", Revision: 1, Digest: digest}}

	r := newReporter()
	require.NoError(t, g.runner.Start(t.Context(), d, r))
	r.wait(t)

	entries, err := os.ReadDir(skillsDir)
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.Equal(t, []string{".system", "pdf"}, names)
}
