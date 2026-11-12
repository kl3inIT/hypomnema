package workertools

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

const dispatchKey = "the-dispatch's-key"

// controlPlane stands for the control plane's agent API: it lists one browser
// tool and answers a call with what `result` holds, or refuses with `refuse`.
type controlPlane struct {
	mu      sync.Mutex
	refuse  string
	result  string
	calls   []agenttool.BrowserCall
	keys    []string
	started chan struct{}
	ended   chan struct{}
}

func (cp *controlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cp.mu.Lock()
	cp.keys = append(cp.keys, r.Header.Get("Authorization"))
	refuse, result := cp.refuse, cp.result
	cp.mu.Unlock()

	if refuse != "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"message":"`+refuse+`"}`)

		return
	}

	switch r.Method + " " + r.URL.Path {
	case "GET " + agenttool.PathBrowserTools:
		_, _ = io.WriteString(w, `{"data":[{"name":"agent_browser_open","description":"Open a page.",`+
			`"inputSchema":{"type":"object","properties":{"url":{"type":"string"}}}}]}`)
	case "POST " + agenttool.PathBrowserCall:
		var call agenttool.BrowserCall

		_ = json.NewDecoder(r.Body).Decode(&call)

		cp.mu.Lock()
		cp.calls = append(cp.calls, call)
		cp.mu.Unlock()

		// A call that waits stays open until the worker's side gives it up.
		if cp.started != nil {
			close(cp.started)
			<-r.Context().Done()
			close(cp.ended)

			return
		}

		_, _ = io.WriteString(w, result)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// browsing is a worker's tools server whose runner recorded the agent API.
func browsing(t *testing.T, cp *controlPlane) *client {
	t.Helper()

	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	require.NoError(t, bridge.SetAgent(dir, bridge.Agent{URL: srv.URL + "/", Key: dispatchKey}))

	return newClientIn(t, dir)
}

func names(t *testing.T, msg map[string]any) []string {
	t.Helper()

	tools, ok := msg["result"].(map[string]any)["tools"].([]any)
	require.True(t, ok, "a list of tools: %v", msg)

	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.(map[string]any)["name"].(string)) //nolint:forcetypeassert // test
	}

	return out
}

// Scenario: a worker lists its tools and takes a screenshot. The browser's
// tools are the control plane's, listed beside the worker's own; a call goes
// to the control plane with the Dispatch's key, and its answer, an image
// here, reaches the worker as the control plane gave it.
func TestAWorkerUsesTheSharedBrowser(t *testing.T) {
	t.Parallel()

	const shot = `{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}],"isError":false}`

	cp := &controlPlane{result: shot}
	c := browsing(t, cp)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})

	list := c.read()
	assert.Equal(t, []string{"ask", "finish", "agent_browser_open"}, names(t, list))

	listed := list["result"].(map[string]any)["tools"].([]any)[2].(map[string]any) //nolint:forcetypeassert // test
	assert.Equal(t, "Open a page.", listed["description"])
	assert.Equal(t, "object", listed["inputSchema"].(map[string]any)["type"]) //nolint:forcetypeassert // test

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "agent_browser_screenshot", "arguments": map[string]any{"fullPage": true},
	}})

	raw, err := json.Marshal(c.read()["result"])
	require.NoError(t, err)
	assert.JSONEq(t, shot, string(raw))

	require.Len(t, cp.calls, 1)
	assert.Equal(t, "agent_browser_screenshot", cp.calls[0].Tool)
	assert.JSONEq(t, `{"fullPage":true}`, string(cp.calls[0].Arguments))
	assert.Equal(t, []string{"Bearer " + dispatchKey, "Bearer " + dispatchKey}, cp.keys)
}

// A control plane that gives no browser leaves the worker its own tools, and
// a browser tool called anyway says why in the control plane's words.
func TestAWorkerWithoutABrowserKeepsItsOwnTools(t *testing.T) {
	t.Parallel()

	c := browsing(t, &controlPlane{refuse: "this control plane has no browser"})

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	assert.Equal(t, []string{"ask", "finish"}, names(t, c.read()))

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "agent_browser_open", "arguments": map[string]any{"url": "example.com"},
	}})

	answer := c.read()
	assert.Equal(t, "this control plane has no browser", text(t, answer))
	assert.Equal(t, true, answer["result"].(map[string]any)["isError"]) //nolint:forcetypeassert // test

	// A worker whose runner recorded no agent API has no browser tool at all.
	alone := newClient(t)

	alone.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	assert.Equal(t, []string{"ask", "finish"}, names(t, alone.read()))

	alone.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "agent_browser_open", "arguments": map[string]any{"url": "example.com"},
	}})
	assert.Contains(t, text(t, alone.read()), "unknown tool")
}

// A browser call the worker's CLI cancels is given up at the control plane
// too, and the call ends.
func TestACancelledBrowserCallEndsAtTheControlPlane(t *testing.T) {
	t.Parallel()

	cp := &controlPlane{started: make(chan struct{}), ended: make(chan struct{})}
	c := browsing(t, cp)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{
		"name": "agent_browser_wait_for_text", "arguments": map[string]any{"text": "Done"},
	}})
	<-cp.started

	//nolint:misspell // the MCP method is spelled this way
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled",
		"params": map[string]any{"requestId": 7}})
	<-cp.ended

	answer := c.read()
	assert.Equal(t, true, answer["result"].(map[string]any)["isError"]) //nolint:forcetypeassert // test
}
