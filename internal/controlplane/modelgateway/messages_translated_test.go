package modelgateway_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// messagesEvents reads an Anthropic event stream: each event's data, in order.
func messagesEvents(t *testing.T, body string) []map[string]any {
	t.Helper()

	var events []map[string]any

	for _, frame := range strings.Split(body, "\n\n") {
		var name, data string

		for line := range strings.SplitSeq(frame, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				name = v
			}

			if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = v
			}
		}

		if data == "" {
			continue
		}

		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &event), data)
		assert.Equal(t, name, event["type"], "the event line names the event")

		events = append(events, event)
	}

	return events
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e["type"].(string)) //nolint:forcetypeassert // every event has a type
	}

	return out
}

// Scenario: Claude Code is pointed at a model the gateway serves from an
// OpenAI-compatible provider. The provider is asked in chat's format, with
// Claude Code's system, its tools as functions and the usage frame asked for;
// its chunks come back to Claude Code as Anthropic's events, a text block then
// a tool call, ending on tool_use with the usage counted as Anthropic counts it.
func TestClaudeCodeIsServedFromAChatProvider(t *testing.T) {
	t.Parallel()

	p, got := provider(t, stream(
		`data: {"id":"chatcmpl-9","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me look."}}]}`+"\n\n",
		`data: {"id":"chatcmpl-9","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1",`+
			`"type":"function","function":{"name":"Bash","arguments":"{\"command\":"}}]}}]}`+"\n\n",
		`data: {"id":"chatcmpl-9","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,`+
			`"function":{"arguments":"\"ls\"}"}}]}}]}`+"\n\n",
		`data: {"id":"chatcmpl-9","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
		`data: {"id":"chatcmpl-9","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":9,`+
			`"prompt_tokens_details":{"cached_tokens":20}}}`+"\n\n",
		"data: [DONE]\n\n",
	))
	g := newChatGateway(t, modelgateway.Config{}, p)

	ask := strings.Replace(claudeCodeAsk, `"sol"`, `"gpt"`, 1)
	status, headers, body := g.send(t, "/v1/messages", ask, claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))

	_, sent := got.last(t)

	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent), &request))
	assert.Equal(t, "up-1", request["model"])
	assert.Equal(t, map[string]any{"include_usage": true}, request["stream_options"])
	assert.Equal(t, []any{
		map[string]any{"role": "system", "content": "You are Claude Code, Anthropic's official CLI for Claude."},
		map[string]any{"role": "user", "content": "hi"},
	}, request["messages"])
	assert.Equal(t, []any{map[string]any{"type": "function", "function": map[string]any{
		"name": "Bash", "parameters": map[string]any{"type": "object"},
	}}}, request["tools"])

	events := messagesEvents(t, body)
	assert.Equal(t, []string{
		"message_start", "content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop",
	}, eventTypes(events))

	assert.Equal(t, "Let me look.", delta(events[2], "text"))
	assert.Equal(t, map[string]any{"type": "tool_use", "id": "call_1", "name": "Bash", "input": map[string]any{}},
		events[4]["content_block"])
	args, _ := delta(events[5], "partial_json").(string)
	assert.JSONEq(t, `{"command":"ls"}`, args)
	assert.Equal(t, "tool_use", delta(events[7], "stop_reason"))
	assert.Equal(t, map[string]any{"input_tokens": 10.0, "output_tokens": 9.0, "cache_read_input_tokens": 20.0},
		events[7]["usage"])

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeOK, usage.Outcome)
	assert.Equal(t, "p1", usage.Provider)
	assert.Equal(t, modelgateway.Tokens{Prompt: 30, Completion: 9, Cached: 20}, usage.Tokens)
}

// Scenario: a tool that asks for a whole answer, carrying a tool call and its
// result from an earlier turn. The provider sees the call as an assistant's
// tool call and the result as a tool message; the client gets one Messages
// answer.
func TestAWholeMessagesAnswerFromAChatProvider(t *testing.T) {
	t.Parallel()

	p, got := provider(t, plain(http.StatusOK, `{"id":"chatcmpl-7","object":"chat.completion","choices":[{"index":0,`+
		`"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":12,"completion_tokens":2}}`))
	g := newChatGateway(t, modelgateway.Config{}, p)

	status, _, body := g.send(t, "/v1/messages", `{"model":"gpt","max_tokens":50,"messages":[`+
		`{"role":"user","content":"list"},`+
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},`+
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"}]}]}`,
		"X-Api-Key", g.key)
	require.Equal(t, http.StatusOK, status, body)

	assert.JSONEq(t, `{"id":"msg_7","type":"message","role":"assistant","model":"gpt",`+
		`"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","stop_sequence":null,`+
		`"usage":{"input_tokens":12,"output_tokens":2}}`, body)

	_, sent := got.last(t)
	assert.JSONEq(t, `{"model":"up-1","stream":false,"max_tokens":50,"messages":[`+
		`{"role":"user","content":"list"},`+
		`{"role":"assistant","tool_calls":[{"id":"toolu_1","type":"function",`+
		`"function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},`+
		`{"role":"tool","tool_call_id":"toolu_1","content":"a.txt"}]}`, sent)
}

// Scenario: counting the tokens of a prompt for a model with no Claude target
// asks no provider; the gateway estimates, as 9router-go and 9router do.
func TestCountingTokensWithoutAClaudeTargetIsAnEstimate(t *testing.T) {
	t.Parallel()

	p, got := provider(t, plain(http.StatusOK, `{}`))
	g := newChatGateway(t, modelgateway.Config{}, p)

	status, _, body := g.send(t, "/v1/messages/count_tokens",
		`{"model":"gpt","messages":[{"role":"user","content":"hello there"}]}`, "X-Api-Key", g.key)
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, `"input_tokens":`)
	assert.Zero(t, got.calls())
}

// Scenario: Claude Code is pointed at a model served from the owner's Codex
// subscription. The answer comes back as Anthropic's events, with the tokens
// Codex reported.
func TestClaudeCodeIsServedFromTheCodexSubscription(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})

	ask := `{"model":"sol","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	status, _, body := g.send(t, "/v1/messages", ask, claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 1, got.calls())

	events := messagesEvents(t, body)
	require.NotEmpty(t, events)
	assert.Equal(t, "message_stop", events[len(events)-1]["type"])
	assert.Contains(t, body, `"text":"hel"`)
	assert.Contains(t, body, `"text":"lo"`)

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeOK, usage.Outcome)
	assert.Equal(t, int64(30), usage.Tokens.Prompt)
	assert.Equal(t, int64(20), usage.Tokens.Cached)
}

// delta is one field of an event's delta.
func delta(event map[string]any, field string) any {
	d, _ := event["delta"].(map[string]any)

	return d[field]
}
