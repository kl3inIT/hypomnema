package toolsmcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// restartingControlPlane is the agent API of a control plane that restarts, as when it updates, while a node_exec
// runs (issue #1060): the call's connection ends with no answer; after it, the node has not reconnected yet, then
// it has, and answers about the process the call named. down keeps it away for good.
type restartingControlPlane struct {
	mu    sync.Mutex
	down  bool
	calls []agenttool.BrowserCall
	asked int
}

func (c *restartingControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var call agenttool.BrowserCall
	_ = json.NewDecoder(r.Body).Decode(&call)

	c.mu.Lock()
	c.calls = append(c.calls, call)
	down := c.down
	first := len(c.calls) == 1
	if call.Tool == agenttool.ToolNodeProcess {
		c.asked++
	}
	asked := c.asked
	c.mu.Unlock()

	if first || down {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}

		return
	}

	text, failed := "exit code 0 (process_id proc_x)\noutput:\nbuilt\n", false
	if asked == 1 {
		text, failed = agenttool.NodeNotConnected("zm", ""), true
	}

	if err := json.NewEncoder(w).Encode(toolsmcp.Text(text, failed)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (c *restartingControlPlane) seen() []agenttool.BrowserCall {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]agenttool.BrowserCall(nil), c.calls...)
}

// resultText is a tool result's first text and whether it failed.
func resultText(t *testing.T, raw json.RawMessage) (string, bool) {
	t.Helper()

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}

	require.NoError(t, json.Unmarshal(raw, &result))
	require.NotEmpty(t, result.Content)

	return result.Content[0].Text, result.IsError
}

// Scenario: the control plane restarts while a Bot's node_exec runs (issue #1060). The tools server named the
// command's process before it asked; once the control plane is back and the node has reconnected, it reads the
// command's result with node_process, waiting as long as the command may run, instead of answering EOF; it never
// runs the command again.
func TestANodeExecAcrossAControlPlaneRestartAnswersTheCommandsResult(t *testing.T) {
	t.Parallel()

	cp := &restartingControlPlane{}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	agent := toolsmcp.NewAgent(srv.URL, "key")

	raw, err := agent.NodeCall(t.Context(), agenttool.PathToolsCall, agenttool.ToolNodeExec,
		json.RawMessage(`{"node":"zm","command":"make build","timeout_seconds":120}`))
	require.NoError(t, err)

	text, failed := resultText(t, raw)
	assert.False(t, failed)
	assert.Contains(t, text, "the control plane restarted while this command ran")
	assert.Contains(t, text, "exit code 0")
	assert.Contains(t, text, "built")

	calls := cp.seen()
	require.Len(t, calls, 3)

	var exec agenttool.NodeExecArgs
	require.NoError(t, json.Unmarshal(calls[0].Arguments, &exec))
	assert.Equal(t, agenttool.ToolNodeExec, calls[0].Tool)
	assert.Equal(t, "make build", exec.Command)
	assert.True(t, nodeproto.IsProcessID(exec.ProcessID), "the tools server names the process before it asks")

	for _, c := range calls[1:] {
		assert.Equal(t, agenttool.ToolNodeProcess, c.Tool, "the command is read again, never run again")

		var asked agenttool.NodeProcessArgs
		require.NoError(t, json.Unmarshal(c.Arguments, &asked))
		assert.Equal(t, agenttool.NodeProcessArgs{Node: "zm", ProcessID: exec.ProcessID, WaitSeconds: 120}, asked)
	}
}

// A control plane that does not come back within the call's time leaves an error that says it restarted and names
// the process to read the command's result with.
func TestANodeExecWhoseControlPlaneStaysAwayNamesItsProcess(t *testing.T) {
	t.Parallel()

	cp := &restartingControlPlane{down: true}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err := toolsmcp.NewAgent(srv.URL, "key").NodeCall(ctx, agenttool.PathToolsCall, agenttool.ToolNodeExec,
		json.RawMessage(`{"node":"zm","command":"make build"}`))
	require.Error(t, err)

	var exec agenttool.NodeExecArgs
	require.NoError(t, json.Unmarshal(cp.seen()[0].Arguments, &exec))

	assert.Contains(t, err.Error(), "the control plane restarted")
	assert.Contains(t, err.Error(), "node_process, process_id "+exec.ProcessID)
	assert.Contains(t, err.Error(), "node zm")
}
