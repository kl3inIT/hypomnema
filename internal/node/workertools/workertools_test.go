package workertools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
)

// client speaks MCP to a server over pipes, as Claude Code does over stdio.
type client struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	dir string
}

func newClient(t *testing.T) *client {
	t.Helper()

	return newClientIn(t, t.TempDir())
}

// newClientIn is a client of the server over the bridge directory dir, with
// what its runner recorded there.
func newClientIn(t *testing.T, dir string) *client {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	s := &server{dir: dir, out: outW, calls: map[string]context.CancelFunc{},
		poll: 10 * time.Millisecond, progress: 30 * time.Millisecond, agent: agentOf(dir)}

	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = s.serve(context.WithoutCancel(t.Context()), inR)
		_ = outW.Close()
	}()

	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})

	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	return &client{t: t, in: inW, out: sc, dir: dir}
}

func (c *client) send(msg map[string]any) {
	c.t.Helper()

	raw, err := json.Marshal(msg)
	require.NoError(c.t, err)

	_, err = c.in.Write(append(raw, '\n'))
	require.NoError(c.t, err)
}

func (c *client) read() map[string]any {
	c.t.Helper()

	require.True(c.t, c.out.Scan(), "the server answers")

	var msg map[string]any
	require.NoError(c.t, json.Unmarshal(c.out.Bytes(), &msg))

	return msg
}

func text(t *testing.T, msg map[string]any) string {
	t.Helper()

	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "a result: %v", msg)

	content, ok := result["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)

	first, ok := content[0].(map[string]any)
	require.True(t, ok)

	s, ok := first["text"].(string)
	require.True(t, ok)

	return s
}

// Scenario: Claude Code connects, lists the tools, and the worker finishes
// with a Gate. The report lands in the bridge for the runner.
func TestFinishIsRecordedInTheBridge(t *testing.T) {
	t.Parallel()

	c := newClient(t)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18"}})
	init := c.read()
	assert.Equal(t, "2025-06-18", init["result"].(map[string]any)["protocolVersion"]) //nolint:forcetypeassert // test

	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})

	list := c.read()
	tools, ok := list["result"].(map[string]any)["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 2)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
		"name": "finish", "arguments": map[string]any{
			"summary":  "added the button; PR #7 is green",
			"evidence": []map[string]string{{"kind": "pull_request", "ref": "kl3inIT/second-bite#7"}},
			"gate":     map[string]any{"question": "Merge PR #7?", "options": []string{"merge", "change first"}},
		},
	}})
	assert.Contains(t, text(t, c.read()), "End your turn now")

	final, ok, err := bridge.Finished(c.dir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "added the button; PR #7 is green", final.Summary)
	require.NotNil(t, final.Gate)
	assert.Equal(t, "Merge PR #7?", final.Gate.Text)
	assert.Equal(t, "owner", final.Gate.To)
	assert.Len(t, final.Gate.Options, 2)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
		"name": "finish", "arguments": map[string]any{},
	}})
	assert.Contains(t, text(t, c.read()), "needs a summary")
}

// Scenario: the worker asks and waits. Progress notifications keep the call
// alive while it waits; the answer the runner writes ends the call.
func TestAskWaitsWithProgressUntilAnswered(t *testing.T) {
	t.Parallel()

	c := newClient(t)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"question": "goth or hand-written?", "options": []string{"goth", "x"}},
		"_meta": map[string]any{"progressToken": "tok_1"},
	}})

	progress := c.read()
	assert.Equal(t, "notifications/progress", progress["method"])
	assert.Equal(t, "tok_1", progress["params"].(map[string]any)["progressToken"]) //nolint:forcetypeassert // test

	qs, err := bridge.Questions(c.dir)
	require.NoError(t, err)
	require.Len(t, qs, 1)
	assert.Equal(t, "goth or hand-written?", qs[0].Ask.Text)
	assert.Equal(t, "owner", qs[0].Ask.To)

	require.NoError(t, bridge.Answer(c.dir, qs[0].Seq, "goth"))

	for {
		msg := c.read()
		if msg["method"] == "notifications/progress" {
			continue
		}

		assert.InDelta(t, 7, msg["id"], 0)
		assert.Equal(t, "The answer: goth", text(t, msg))

		break
	}
}

// Scenario: Claude Code cancels a waiting ask (an interrupt). The call ends
// at once, saying the question was withdrawn.
func TestCancelledAskEnds(t *testing.T) {
	t.Parallel()

	c := newClient(t)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"question": "anything?"},
	}})
	//nolint:misspell // the MCP method is spelled this way
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 9}})

	assert.Equal(t, "The question was withdrawn.", text(t, c.read()))
}
