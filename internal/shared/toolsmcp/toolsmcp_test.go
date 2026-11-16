package toolsmcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// controlPlane is a Bot's agent API: the tools its settings give, which the
// owner changes while the chat's pi runs.
type controlPlane struct {
	mu     sync.Mutex
	tools  []agenttool.BrowserTool
	broken bool
}

func (c *controlPlane) set(broken bool, names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.broken = broken
	c.tools = nil

	for _, n := range names {
		c.tools = append(c.tools, agenttool.BrowserTool{Name: n, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
}

func (c *controlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch r.URL.Path {
	case agenttool.PathTools:
		if c.broken {
			w.WriteHeader(http.StatusBadGateway)

			return
		}

		if err := json.NewEncoder(w).Encode(agenttool.BrowserToolList{Data: c.tools}); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
	case agenttool.PathToolsCall:
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}]}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// pi speaks MCP to the server over pipes, as a Bot's pi does over stdio.
type pi struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

func startPi(t *testing.T, cp *controlPlane) *pi {
	t.Helper()

	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	agent := toolsmcp.NewAgent(srv.URL, "token")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = toolsmcp.Serve(context.WithoutCancel(t.Context()), inR, outW, toolsmcp.Config{
			Name: "hypomnema",
			List: func(ctx context.Context) ([]map[string]any, error) {
				return agent.Tools(ctx, agenttool.PathTools)
			},
			Call: func(ctx context.Context, c toolsmcp.Call) (any, error) {
				return agent.ToolCall(ctx, agenttool.PathToolsCall, toolsmcp.CallTimeout, c.Name, c.Arguments)
			},
		})
		_ = outW.Close()
	}()

	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})

	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	return &pi{t: t, in: inW, out: sc}
}

// request sends one request and returns what the server wrote up to its answer: notifications first.
func (p *pi) request(method string, params any) (notes []string, result map[string]any) {
	p.t.Helper()

	p.id++

	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p.id, "method": method, "params": params})
	require.NoError(p.t, err)

	_, err = p.in.Write(append(raw, '\n'))
	require.NoError(p.t, err)

	for {
		require.True(p.t, p.out.Scan(), "the server answers")

		var msg struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Result map[string]any `json:"result"`
		}
		require.NoError(p.t, json.Unmarshal(p.out.Bytes(), &msg))

		if msg.ID == nil {
			notes = append(notes, msg.Method)

			continue
		}

		require.Equal(p.t, p.id, *msg.ID)

		return notes, msg.Result
	}
}

func (p *pi) toolNames() []string {
	p.t.Helper()

	_, result := p.request("tools/list", map[string]any{})

	var names []string

	tools, ok := result["tools"].([]any)
	require.True(p.t, ok, "a tools/list answer: %v", result)

	for _, tool := range tools {
		m, ok := tool.(map[string]any)
		require.True(p.t, ok)

		names = append(names, m["name"].(string)) //nolint:forcetypeassert // test
	}

	return names
}

func (p *pi) call(name string) []string {
	p.t.Helper()

	notes, result := p.request("tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
	assert.Contains(p.t, result["content"], map[string]any{"type": "text", "text": "done"}, "the call is answered")

	return notes
}

// Scenario (issue #653): the owner gives a Bot a WhatsApp Connection while
// its chat's pi runs. The Bot's next tool call tells pi that the list
// changed, and pi's next tools/list has whatsapp_messages. A list that is
// unchanged, or that the control plane failed to give, tells pi nothing.
func TestAConnectionGivenMidChatReachesTheRunningPi(t *testing.T) {
	t.Parallel()

	cp := &controlPlane{}
	cp.set(false, "memory")

	p := startPi(t, cp)

	_, init := p.request("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	assert.Equal(t, map[string]any{"tools": map[string]any{"listChanged": true}}, init["capabilities"])
	assert.Equal(t, []string{"memory"}, p.toolNames())

	assert.Empty(t, p.call("memory"), "nothing changed")

	cp.set(true)
	assert.Empty(t, p.call("memory"), "a list that failed is not a change")

	cp.set(false, "memory", "whatsapp_messages")
	assert.Equal(t, []string{"notifications/tools/list_changed"}, p.call("memory"))
	assert.Empty(t, p.call("memory"), "told once")
	assert.Equal(t, []string{"memory", "whatsapp_messages"}, p.toolNames())
	assert.Empty(t, p.call("whatsapp_messages"))
}
