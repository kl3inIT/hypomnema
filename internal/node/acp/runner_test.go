package acp

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
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The test binary plays three parts: the tests, the shim (`shim --dir D`),
// and a fake agent (FAKE_ACP set) that speaks ACP on its input and output.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shim" && os.Args[2] == "--dir" {
		if err := shim.Main(context.Background(), os.Args[3]); err != nil {
			os.Exit(2)
		}

		os.Exit(0)
	}

	if mode := os.Getenv("FAKE_ACP"); mode != "" {
		os.Exit(fakeAgent(mode))
	}

	os.Exit(m.Run())
}

type rpc struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		SessionID  string `json:"sessionId"`
		Cwd        string `json:"cwd"`
		MCPServers []struct {
			Name string   `json:"name"`
			Args []string `json:"args"`
		} `json:"mcpServers"`
		Prompt []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	} `json:"params"`
	Result struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	} `json:"result"`
}

// fakeAgent answers as an ACP agent does. It records the methods it was
// called with, its arguments and its environment in its cwd.
func fakeAgent(mode string) int {
	//nolint:gosec // fixed names in the cwd
	_ = os.WriteFile("argv.txt", []byte(strings.Join(os.Args[1:], "\n")), 0o600)
	//nolint:gosec // fixed names in the cwd
	_ = os.WriteFile("env.txt", []byte(os.Getenv(keyVariable)+"\n"+os.Getenv("PI_CODING_AGENT_DIR")+"\n"+
		os.Getenv("OPENCODE_CONFIG")), 0o600)

	out := json.NewEncoder(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 4<<20)

	var (
		methods []string
		dir     string
		prompt  json.RawMessage
	)

	record := func(method string) {
		methods = append(methods, method)
		_ = os.WriteFile("methods.txt", []byte(strings.Join(methods, "\n")), 0o600)
	}

	answer := func(id json.RawMessage, result map[string]any) {
		emit(out, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}

	finish := func() {
		emit(out, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "ses_fake", "update": map[string]any{
				"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Done."},
			},
		}})

		if mode != "nofinish" {
			_ = bridge.Finish(dir, bridge.Final{
				Summary:  "added the button",
				Evidence: []nodeproto.Evidence{{Kind: "pull_request", Ref: "kl3inIT/second-bite#7"}},
			})
		}

		answer(prompt, map[string]any{"stopReason": "end_turn", "usage": map[string]any{
			"inputTokens": 1200, "outputTokens": 300, "cachedReadTokens": 9000,
		}})
	}

	for in.Scan() {
		var m rpc
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}

		if m.Method != "" {
			record(m.Method)
		}

		switch m.Method {
		case "initialize":
			caps := map[string]any{"loadSession": true}
			if mode != "loads" {
				caps["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			}

			answer(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": caps})
		case "session/resume", "session/load":
			if mode == "gone" {
				emit(out, map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{
					"code": -32602, "message": "Invalid params", "data": map[string]any{"details": "no such session"},
				}})

				continue
			}

			dir = m.Params.MCPServers[0].Args[2]
			_ = os.WriteFile("resumed.txt", []byte(m.Params.SessionID), 0o600)
			answer(m.ID, map[string]any{})
		case "session/new":
			dir = m.Params.MCPServers[0].Args[2]
			_ = os.WriteFile("cwd.txt", []byte(m.Params.Cwd), 0o600)
			answer(m.ID, map[string]any{"sessionId": "ses_fake"})
		case "session/prompt":
			prompt = m.ID
			_ = os.WriteFile("prompt.txt", []byte(m.Params.Prompt[0].Text), 0o600)

			switch mode {
			case "hang":
			case "limited":
				emit(out, map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{
					"code": -32603, "message": "Internal error", "data": map[string]any{"errorName": "provider.rate-limit"},
				}})
			case "said":
				// omp: the model's refusal is a message, and the turn ends as any other.
				emit(out, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"sessionId": "ses_fake", "update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "401 Unauthorized: invalid_api_key"},
					},
				}})
				answer(m.ID, map[string]any{"stopReason": "end_turn"})
			case "asks":
				emit(out, map[string]any{"jsonrpc": "2.0", "id": 77, "method": "session/request_permission",
					"params": map[string]any{
						"sessionId": "ses_fake", "toolCall": map[string]any{"title": "bash"},
						"options": []map[string]any{
							{"optionId": "no", "kind": "reject_once"}, {"optionId": "once", "kind": "allow_once"},
							{"optionId": "always", "kind": "allow_always"},
						},
					}})
			default:
				finish()
			}
		case "session/cancel":
			answer(prompt, map[string]any{"stopReason": "cancelled"}) //nolint:misspell // ACP's own spelling
		case "":
			// The client's answer to the permission request.
			_ = os.WriteFile("permission.txt", []byte(m.Result.Outcome.Outcome+" "+m.Result.Outcome.OptionID), 0o600)
			finish()
		}
	}

	return 0
}

func emit(out *json.Encoder, v map[string]any) {
	if err := out.Encode(v); err != nil {
		os.Exit(4)
	}
}

// reporter records what the Supervisor reports.
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
	sup      *runner.Supervisor
	rt       *ACPRuntime
	worktree string
}

func newRig(t *testing.T, mode string) *rig {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	home := t.TempDir()

	return &rig{
		t: t, worktree: filepath.Join(t.TempDir(), "wt"),
		sup: &runner.Supervisor{Home: home, Exe: exe, Logger: slog.New(slog.DiscardHandler)},
		rt: &ACPRuntime{
			Home: home, Exe: exe, Paths: map[string]string{"omp": exe, "opencode": exe},
			URL: "https://hypomnema.example/", Env: []string{"FAKE_ACP=" + mode},
		},
	}
}

func (g *rig) dispatch(runtime, session string) statedb.Dispatch {
	return statedb.Dispatch{
		ID: "dsp_1", State: statedb.StateAccepted, Epoch: 1,
		Offer: nodeproto.Offer{
			DispatchID: "dsp_1", TaskID: "tsk_1", RunID: "run_1", Profile: "implementer",
			Spec: "add the Google login button", ConversationID: "cnv_1", Worktree: g.worktree, Runtime: runtime,
			AccountRef: "gateway", Model: "claude-sonnet", Effort: "high", ModelKey: "hyp_gateway_v1_test",
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

func (g *rig) run(runtime, session string) *reporter {
	g.t.Helper()

	r := newReporter()
	require.NoError(g.t, g.sup.Start(g.t.Context(), g.rt, g.dispatch(runtime, session), r))
	r.wait(g.t)

	return r
}

// Scenario: an omp worker's turn that succeeds. omp is started in ACP mode
// with the gateway as its provider, in an agent directory of the
// Conversation's own, the key only in its environment. The conversation is
// initialize, a new session in the worktree with the tools server, the
// prompt; the session id is reported, and finish's report with the usage.
func TestOmpThroughTheGateway(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	r := g.run("omp", "")

	assert.Equal(t, []string{"shim ses_fake"}, r.started)
	require.Len(t, r.finished, 1)

	f := r.finished[0]
	assert.Empty(t, f.Class)
	assert.Equal(t, "added the button", f.Summary)
	assert.Equal(t, &nodeproto.Usage{Input: 1200, Output: 300, CacheRead: 9000}, f.Usage)

	format, err := os.ReadFile(filepath.Join(g.rt.Home, "run", "dsp_1", "format"))
	require.NoError(t, err)
	assert.Equal(t, "acp", string(format), "the journal says how it is to be read")

	conversation := filepath.Join(g.rt.Home, "conversations", "cnv_1", "omp")
	assert.Equal(t, "acp\n--model\nhypomnema/claude-sonnet\n--session-dir\n"+filepath.Join(conversation, "sessions")+
		"\n--thinking\nhigh", g.file("argv.txt"))
	assert.NotContains(t, g.file("argv.txt"), "hyp_gateway_v1_test")

	env := strings.Split(g.file("env.txt"), "\n")
	assert.Equal(t, "hyp_gateway_v1_test", env[0])
	assert.Equal(t, filepath.Join(conversation, "agent"), env[1])

	models, err := os.ReadFile(filepath.Join(conversation, "agent", "models.yml")) //nolint:gosec // the test's own
	require.NoError(t, err)
	assert.Contains(t, string(models), `baseUrl: "https://hypomnema.example/v1"`)
	assert.Contains(t, string(models), "apiKey: HYPOMNEMA_MODEL_KEY")
	assert.Contains(t, string(models), "api: openai-completions")
	assert.NotContains(t, string(models), "hyp_gateway_v1_test", "the key is in no file")

	config, err := os.ReadFile(filepath.Join(conversation, "agent", "config.yml")) //nolint:gosec // the test's own
	require.NoError(t, err)
	assert.Contains(t, string(config), "xdev: false")
	assert.Contains(t, string(config), "web_search:\n  enabled: false\n", "omp's own search is off")
	assert.Contains(t, string(models), "      - id: auto\n        name: \"Images through Hypomnema\"\n"+
		"        api: openai-images\n", "the gateway is omp's image model (issue #431)")
	assert.Contains(t, string(config), "modelRoles:\n  image: \"hypomnema/auto\"\n", "and its image role")

	assert.Equal(t, "initialize\nsession/new\nsession/prompt", g.file("methods.txt"))
	assert.Equal(t, g.worktree, g.file("cwd.txt"))
	assert.Contains(t, g.file("prompt.txt"), "add the Google login button")
}

// Scenario: an opencode worker. Its configuration is a file of the
// Conversation's own, away from the user's and the project's, that names
// the gateway with the key as an environment variable's name, allows every
// action and denies the tool that would hide the tools server's tools, and
// its own web tools.
func TestOpencodeThroughTheGateway(t *testing.T) {
	t.Parallel()

	g := newRig(t, "success")
	r := g.run("opencode", "")

	require.Len(t, r.finished, 1)
	assert.Empty(t, r.finished[0].Class)
	assert.Equal(t, "acp", g.file("argv.txt"))

	conversation := filepath.Join(g.rt.Home, "conversations", "cnv_1", "opencode")
	assert.Equal(t, filepath.Join(conversation, "opencode.json"), strings.Split(g.file("env.txt"), "\n")[2])

	raw, err := os.ReadFile(filepath.Join(conversation, "opencode.json")) //nolint:gosec // the test's own directory
	require.NoError(t, err)

	var config struct {
		Model     string `json:"model"`
		Providers map[string]struct {
			Settings map[string]string `json:"settings"`
			Models   map[string]struct {
				Type string `json:"type"`
			} `json:"models"`
		} `json:"providers"`
		Permissions  []map[string]string `json:"permissions"`
		Experimental struct {
			ImageGenerate struct {
				Model string `json:"model"`
			} `json:"image_generate"`
		} `json:"experimental"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))

	assert.Equal(t, "hypomnema/claude-sonnet", config.Model)
	//nolint:gosec // G101: the name of an environment variable, not a credential
	assert.Equal(t, map[string]string{
		"baseURL": "https://hypomnema.example/v1", "apiKey": "{env:HYPOMNEMA_MODEL_KEY}",
	}, config.Providers["hypomnema"].Settings)
	assert.Contains(t, config.Permissions, map[string]string{"action": "execute", "resource": "*", "effect": "deny"})

	for _, own := range []string{"websearch", "webfetch"} {
		assert.Contains(t, config.Permissions, map[string]string{"action": own, "resource": "*", "effect": "deny"},
			"opencode's own %s is off", own)
	}
	assert.NotContains(t, string(raw), `"skills"`, "a worker given no skill is told of no folder")
	assert.Equal(t, "image", config.Providers["hypomnema"].Models["auto"].Type,
		"the gateway is opencode's image model (issue #431)")
	assert.Equal(t, "hypomnema/auto", config.Experimental.ImageGenerate.Model)
}

// Scenario: a follow-up Dispatch of a Conversation. The agent resumes its
// session when it can, loads it when that is all it offers, and when the
// session is gone a new one is opened and the turn goes on.
func TestAFollowUpResumesTheSession(t *testing.T) {
	t.Parallel()

	for mode, want := range map[string]string{
		"success": "initialize\nsession/resume\nsession/prompt",
		"loads":   "initialize\nsession/load\nsession/prompt",
		"gone":    "initialize\nsession/resume\nsession/new\nsession/prompt",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			g := newRig(t, mode)
			r := g.run("omp", "ses_before")

			assert.Equal(t, want, g.file("methods.txt"))
			require.Len(t, r.finished, 1)
			assert.Empty(t, r.finished[0].Class)

			session := "ses_before"
			if mode == "gone" {
				session = "ses_fake"
			} else {
				assert.Equal(t, "ses_before", g.file("resumed.txt"))
			}

			assert.Equal(t, []string{"shim " + session}, r.started)
		})
	}
}

// Scenario: how a turn that does not finish is reported, and a permission
// request. opencode names a rate limit in a JSON-RPC error; omp says a
// refused key in its message and ends the turn as any other; a turn without
// finish is the worker's failure. A permission request is answered with the
// option that allows for good, and the turn goes on.
func TestHowAnACPTurnEnds(t *testing.T) {
	t.Parallel()

	for mode, want := range map[string]string{
		"limited": "rate_limited", "said": "needs_auth", "nofinish": "worker_error",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, mode).run("opencode", "")
			require.Len(t, r.finished, 1)
			assert.Equal(t, want, r.finished[0].Class, r.finished[0].Summary)
		})
	}

	t.Run("asks", func(t *testing.T) {
		t.Parallel()

		g := newRig(t, "asks")
		r := g.run("opencode", "")

		assert.Equal(t, "selected always", g.file("permission.txt"))
		require.Len(t, r.finished, 1)
		assert.Empty(t, r.finished[0].Class)
	})

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		g := newRig(t, "hang")
		r := newReporter()
		require.NoError(t, g.sup.Start(t.Context(), g.rt, g.dispatch("omp", ""), r))

		require.Eventually(t, func() bool {
			_, err := os.Stat(filepath.Join(g.worktree, "prompt.txt"))

			return err == nil
		}, 10*time.Second, 20*time.Millisecond)

		g.sup.Stop("dsp_1")
		r.wait(t)
		assert.Equal(t, 1, r.stopped)
		assert.Contains(t, g.file("methods.txt"), "session/cancel")
	})

	t.Run("no key", func(t *testing.T) {
		t.Parallel()

		g := newRig(t, "success")
		d := g.dispatch("omp", "")
		d.Offer.ModelKey = ""

		require.ErrorContains(t, g.sup.Start(t.Context(), g.rt, d, newReporter()), "no key of the model gateway")
	})
}

// Scenario: an omp and an opencode worker are given a skill the node has
// kept (issue #21). omp finds it in its agent directory's skills; opencode is
// told of its folder in its configuration.
func TestOmpAndOpencodeFindTheirSkills(t *testing.T) {
	t.Parallel()

	for _, runtime := range []string{"omp", "opencode"} {
		t.Run(runtime, func(t *testing.T) {
			t.Parallel()

			g := newRig(t, "success")
			digest := strings.Repeat("ef", 32)
			kept := filepath.Join(g.rt.Home, "skills", digest)
			require.NoError(t, os.MkdirAll(kept, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(kept, "SKILL.md"), []byte("---\nname: pdf\n---\n"), 0o600))

			d := g.dispatch(runtime, "")
			d.Offer.Skills = []nodeproto.Skill{{Name: "pdf", Revision: 1, Digest: digest}}

			r := newReporter()
			require.NoError(t, g.sup.Start(t.Context(), g.rt, d, r))
			r.wait(t)

			conversation := filepath.Join(g.rt.Home, "conversations", "cnv_1", runtime)
			where := map[string]string{
				"omp": filepath.Join(conversation, "agent", "skills"), "opencode": filepath.Join(conversation, "skills"),
			}[runtime]
			assert.FileExists(t, filepath.Join(where, "pdf", "SKILL.md"))

			if runtime != "opencode" {
				return
			}

			raw, err := os.ReadFile(filepath.Join(conversation, "opencode.json")) //nolint:gosec // the test's own
			require.NoError(t, err)

			var config struct {
				Skills []string `json:"skills"`
			}
			require.NoError(t, json.Unmarshal(raw, &config))
			assert.Equal(t, []string{where}, config.Skills)
		})
	}
}
