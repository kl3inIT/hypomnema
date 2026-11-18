//go:build integration

package claude

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
		closeInput    bool
	}{
		{name: "completion", command: completionCommand, input: `"run_in_background":true`,
			notification: `"status":"completed"`, ceiling: completionCeiling, wait: completionWait},
		{name: "timeout", command: "sleep 9", input: `"run_in_background":true,"timeout":6500`,
			notification: `"status":"stopped"`, ceiling: "5000", wait: 20 * time.Second},
		{name: "closed input", command: "sleep 7", input: `"run_in_background":true`, notification: `"status":"completed"`,
			ceiling: "5000", wait: 20 * time.Second, closeInput: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			g := newRig(t, "")
			api := backgroundModel(t, test.command, test.input)
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

func backgroundModel(t *testing.T, command, input string) *httptest.Server {
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

		serveClaudeMessage(w, index, name, args, text)
	}))
	t.Cleanup(server.Close)

	return server
}

func serveClaudeMessage(w http.ResponseWriter, index int32, name, args, text string) {
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
		frame("content_block_delta", fmt.Sprintf(`,"index":0,"delta":{"type":"input_json_delta","partial_json":%q}`, args))
		stop = "tool_use"
	}
	frame("content_block_stop", `,"index":0`)
	frame("message_delta", fmt.Sprintf(`,"delta":{"stop_reason":%q,"stop_sequence":null},`+
		`"usage":{"output_tokens":1}`, stop))
	frame("message_stop", "")
}
