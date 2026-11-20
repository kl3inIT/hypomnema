//go:build integration

package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/shim"
)

func TestRealClaudeBackgroundBash(t *testing.T) {
	t.Parallel()

	name := os.Getenv("HYPOMNEMA_TEST_CLAUDE")
	if name == "" {
		t.Skip("set HYPOMNEMA_TEST_CLAUDE=claude to run with a local Claude Code CLI")
	}
	cli, err := exec.LookPath(name)
	require.NoError(t, err)
	cli, err = filepath.Abs(cli)
	require.NoError(t, err)

	completionCommand, completionCeiling, completionWait := "sleep 605", "600000", 11*time.Minute
	if testing.Short() {
		completionCommand, completionCeiling, completionWait = "sleep 7", "5000", 20*time.Second
	}

	for _, test := range []struct {
		name, command string
		input         string
		notification  string
		ceiling       string
		wait          time.Duration
		slowStream    bool
		closeInput    bool
		killTree      bool
	}{
		{name: "completion", command: completionCommand, input: `"run_in_background":true`,
			notification: `"status":"completed"`, ceiling: completionCeiling, wait: completionWait},
		{name: "timeout", command: "sleep 9", input: `"run_in_background":true,"timeout":6500`,
			notification: `"status":"stopped"`, ceiling: "5000", wait: 20 * time.Second},
		{name: "closed input", command: "sleep 7", input: `"run_in_background":true`, notification: `"status":"completed"`,
			ceiling: "5000", wait: 20 * time.Second, closeInput: true},
		{name: "slow stream", command: "sleep 7", input: `"run_in_background":true`, notification: `"status":"completed"`,
			ceiling: "5000", wait: 20 * time.Second, slowStream: true},
		{name: "kill detached Bash", command: "echo $$ > background-pid; sleep 4; touch survived-kill",
			input: `"run_in_background":true`, wait: 20 * time.Second, killTree: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.killTree && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				t.Skip("the detached Bash scenario uses Unix process sessions")
			}
			g := newRig(t, "")
			api := backgroundModel(t, test.command, test.input, test.slowStream)
			g.rt.Claude, g.rt.URL = cli, api.URL
			g.rt.Env = []string{
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ENABLE_TOOL_SEARCH=false",
				"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=" + test.ceiling, "TMPDIR=" + t.TempDir(),
			}
			r := newReporter("dsp_1")
			dispatch := g.dispatch(r.ID())
			dispatch.Offer.Model, dispatch.Offer.Effort = "claude-sonnet-4-6", ""
			require.NoError(t, g.sup.Start(t.Context(), g.rt, dispatch, r))
			t.Cleanup(func() { g.sup.Stop(r.ID()) })
			runDir := filepath.Join(g.rt.Home, "run", r.ID())
			root, err := os.OpenRoot(runDir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			if test.killTree {
				testDetachedBashCleanup(t, g, r, root, runDir)
				return
			}
			if test.closeInput {
				require.Eventually(t, func() bool {
					journal, err := root.ReadFile(shim.JournalFile)
					return err == nil && strings.Contains(string(journal), `"result":"waiting"`)
				}, 5*time.Second, 25*time.Millisecond)
				require.NoError(t, shim.Send(t.Context(), runDir, []byte(shim.CloseLine)))
			}
			select {
			case <-r.done:
			case <-time.After(test.wait):
				t.Fatal("the worker did not end")
			}

			journal, err := root.ReadFile(shim.JournalFile)
			require.NoError(t, err)
			stderr, err := root.ReadFile(shim.StderrFile)
			require.NoError(t, err)
			require.Len(t, r.finished, 1, "journal: %s\nstderr: %s", journal, stderr)
			assert.Empty(t, r.finished[0].Class, "journal: %s\nstderr: %s", journal, stderr)
			assert.Equal(t, "finished after Bash", r.finished[0].Summary)
			assert.Contains(t, string(journal), `"result":"waiting"`)
			assert.Contains(t, string(journal), test.notification)
			if test.closeInput {
				assert.NoFileExists(t, filepath.Join(g.worktree, "after-background"))
				assert.Contains(t, string(journal), "Tool permission request failed: AbortError: Stream closed")
				assert.NotContains(t, string(journal), "Recorded. End your turn now")
				return
			}
			assert.FileExists(t, filepath.Join(g.worktree, "after-background"))
			assert.Contains(t, string(journal), "Recorded. End your turn now")
			assert.NotContains(t, string(journal), "Stream closed")
		})
	}
}

func testDetachedBashCleanup(t *testing.T, g *rig, r *reporter, root *os.Root, runDir string) {
	t.Helper()
	require.Eventually(t, func() bool {
		journal, err := root.ReadFile(shim.JournalFile)
		return err == nil && strings.Contains(string(journal), `"result":"waiting"`)
	}, 5*time.Second, 25*time.Millisecond)
	require.FileExists(t, filepath.Join(g.worktree, "background-pid"))
	require.NoError(t, shim.Stop(t.Context(), runDir, "integration test cleanup"))
	r.wait(t)
	require.Len(t, r.finished, 1)
	assert.Equal(t, "signaled", r.finished[0].Exit.Kind)
	assert.Never(t, func() bool {
		_, err := os.Stat(filepath.Join(g.worktree, "survived-kill"))
		return err == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func backgroundModel(t *testing.T, command, input string, slowStream bool) *httptest.Server {
	t.Helper()
	round := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{}`)

			return
		}

		index := round.Add(1)
		name, args, text := "", "", "done"
		switch index {
		case 1:
			name, args = "Bash", fmt.Sprintf(`{"command":%q,%s}`, command, input)
		case 2:
			text = "waiting"
		case 3:
			name, args = "Bash", `{"command":"touch after-background"}`
		case 4:
			name, args = "mcp__hypomnema__finish", `{"summary":"finished after Bash"}`
		}

		serveClaudeMessage(w, index, name, args, text, slowStream && index == 3)
	}))
	t.Cleanup(server.Close)

	return server
}

func serveClaudeMessage(w http.ResponseWriter, index int32, name, args, text string, slowStream bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	frame := func(kind, body string) {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q%s}\n\n", kind, kind, body)
	}
	frame("message_start", fmt.Sprintf(`,"message":{"id":"msg_%d","type":"message","role":"assistant",`+
		`"model":"claude-sonnet-4-6","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}`, index))
	stop := "end_turn"
	if name == "" {
		frame("content_block_start", `,"index":0,"content_block":{"type":"text","text":""}`)
		frame("content_block_delta", fmt.Sprintf(`,"index":0,"delta":{"type":"text_delta","text":%q}`, text))
	} else {
		frame("content_block_start", fmt.Sprintf(`,"index":0,"content_block":`+
			`{"type":"tool_use","id":"toolu_%d","name":%q,"input":{}}`, index, name))
		if slowStream {
			for range 8 {
				frame("content_block_delta", `,"index":0,"delta":{"type":"input_json_delta","partial_json":" "}`)
				if http.NewResponseController(w).Flush() != nil {
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
		frame("content_block_delta", fmt.Sprintf(`,"index":0,"delta":{"type":"input_json_delta","partial_json":%q}`, args))
		stop = "tool_use"
	}
	frame("content_block_stop", `,"index":0`)
	frame("message_delta", fmt.Sprintf(`,"delta":{"stop_reason":%q,"stop_sequence":null},`+
		`"usage":{"output_tokens":1}`, stop))
	frame("message_stop", "")
}

// A planner hands work to an Agent in the background, as Bash is: its result comes with no queued turn, and the
// Agent's end starts the follow-up that still needs the permission stream.
func TestRealClaudeBackgroundAgent(t *testing.T) {
	t.Parallel()

	name := os.Getenv("HYPOMNEMA_TEST_CLAUDE")
	if name == "" {
		t.Skip("set HYPOMNEMA_TEST_CLAUDE=claude to run with a local Claude Code CLI")
	}
	cli, err := exec.LookPath(name)
	require.NoError(t, err)
	cli, err = filepath.Abs(cli)
	require.NoError(t, err)

	g := newRig(t, "")
	api := agentModel(t)
	g.rt.Claude, g.rt.URL = cli, api.URL
	g.rt.Env = []string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ENABLE_TOOL_SEARCH=false",
		"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=5000", "TMPDIR=" + t.TempDir(),
	}
	r := newReporter("dsp_1")
	dispatch := g.dispatch(r.ID())
	dispatch.Offer.Model, dispatch.Offer.Effort = "claude-sonnet-4-6", ""
	// A role-routed planner is the worker that may use Agent.
	dispatch.Offer.RoleRouted, dispatch.Offer.Profile = true, "planner"
	require.NoError(t, g.sup.Start(t.Context(), g.rt, dispatch, r))
	t.Cleanup(func() { g.sup.Stop(r.ID()) })
	runDir := filepath.Join(g.rt.Home, "run", r.ID())
	select {
	case <-r.done:
	case <-time.After(40 * time.Second):
		t.Fatal("the worker did not end")
	}

	journal, err := os.ReadFile(filepath.Join(runDir, shim.JournalFile)) //nolint:gosec // the test's own run directory
	require.NoError(t, err)
	require.Len(t, r.finished, 1, "journal: %s", journal)
	assert.Empty(t, r.finished[0].Class, "journal: %s", journal)
	assert.Equal(t, "finished after the Agent", r.finished[0].Summary)
	assert.Contains(t, string(journal), `"task_type":"local_agent"`)
	assert.Contains(t, string(journal), `"result":"waiting"`)
	assert.FileExists(t, filepath.Join(g.worktree, "after-agent"))
	assert.NotContains(t, string(journal), "Stream closed")
}

const agentTask = "AGENT_TASK: wait, then report"

// agentModel answers the planner's conversation and its Agent's apart: an Agent's conversation opens with its
// prompt.
func agentModel(t *testing.T) *httptest.Server {
	t.Helper()
	planner, agent := atomic.Int32{}, atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{}`)

			return
		}
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if len(body.Messages) > 0 && strings.Contains(string(body.Messages[0].Content), "AGENT_TASK") {
			index := agent.Add(1)
			if index == 1 {
				serveClaudeMessage(w, 100+index, "Bash", `{"command":"sleep 6"}`, "", false)
			} else {
				serveClaudeMessage(w, 100+index, "", "", "agent done", false)
			}

			return
		}

		index := planner.Add(1)
		name, args, text := "", "", "done"
		switch index {
		case 1:
			name, args = "Agent", fmt.Sprintf(`{"description":"plan","prompt":%q,"subagent_type":"general-purpose",`+
				`"run_in_background":true}`, agentTask)
		case 2:
			text = "waiting"
		case 3:
			name, args = "Bash", `{"command":"touch after-agent"}`
		case 4:
			name, args = "mcp__hypomnema__finish", `{"summary":"finished after the Agent"}`
		}
		serveClaudeMessage(w, index, name, args, text, false)
	}))
	t.Cleanup(server.Close)

	return server
}
