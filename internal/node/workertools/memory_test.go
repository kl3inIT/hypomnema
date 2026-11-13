package workertools

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: a worker keeps a fact in its agent's memory, then tries one that
// would pass the limit. The change goes to the control plane with the
// Dispatch's key; the result says how full the memory is, and from 80% asks
// the worker to make room; a refusal is the worker's to fix, in the control
// plane's words.
func TestAWorkerKeepsAFactInItsAgentsMemory(t *testing.T) {
	t.Parallel()

	var changes []agenttool.MemoryChange

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST "+agenttool.PathMemory, r.Method+" "+r.URL.Path)
		assert.Equal(t, "Bearer "+dispatchKey, r.Header.Get("Authorization"))

		var c agenttool.MemoryChange
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&c))
		changes = append(changes, c)

		if len(changes) == 2 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"message":"the memory would be 2300 characters, over its limit of 2200"}`)

			return
		}

		_, _ = io.WriteString(w, `{"memory":"The owner reviews on Fridays.","used":1850,"limit":2200}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	require.NoError(t, bridge.SetAgent(dir, bridge.Agent{URL: srv.URL, Key: dispatchKey}))
	c := newClientIn(t, dir)

	call := func(id int, args map[string]any) map[string]any {
		c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
			"name": "memory", "arguments": args,
		}})

		return c.read()
	}

	saved := call(1, map[string]any{"target": "agent", "action": "add", "text": "The owner reviews on Fridays."})
	assert.Equal(t, "Saved. memory: 1,850 of 2,200 characters (84%). It is nearly full: merge entries that say "+
		"the same thing and remove stale or wrong ones now, until it is under 70% (1,540 characters).",
		text(t, saved))

	refused := call(2, map[string]any{"target": "owner", "action": "add", "text": "A long fact."})
	assert.Equal(t, "the memory would be 2300 characters, over its limit of 2200", text(t, refused))
	assert.Equal(t, true, refused["result"].(map[string]any)["isError"]) //nolint:forcetypeassert // test

	assert.Equal(t, []agenttool.MemoryChange{
		{Target: "agent", Action: "add", Text: "The owner reviews on Fridays."},
		{Target: "owner", Action: "add", Text: "A long fact."},
	}, changes)
}

// A worker whose runner gave no agent API has no memory tool, and a call
// says why.
func TestAWorkerWithoutTheAgentAPIHasNoMemory(t *testing.T) {
	t.Parallel()

	c := newClient(t)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "memory", "arguments": map[string]any{"target": "agent", "action": "add", "text": "x"},
	}})
	assert.Contains(t, text(t, c.read()), "no agent API")
}

func TestHowFullAMemoryIsIsSaid(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "memory: 0 of 1,375 characters (0%)", agenttool.MemoryUsage(0, 1375))
	assert.Equal(t, "memory: 1,759 of 2,200 characters (79%)", agenttool.MemoryUsage(1759, 2200))
	assert.Contains(t, agenttool.MemoryUsage(1760, 2200), "(80%). It is nearly full")
}
