package responseschat_test

import (
	"bufio"
	"embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/responseschat"
)

// translate turns a Responses request into chat and returns the chat body, decoded, and the request.
func translate(t *testing.T, responses string) (map[string]any, responseschat.Request) {
	t.Helper()

	req, err := responseschat.ChatFromResponses([]byte(responses))
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &body))

	return body, req
}

// A Codex CLI's second turn to a chat model: its instructions, a developer message, the user's text and image, the
// model's reasoning and tool calls of the first turn and their outputs, and Codex's tools.
const secondTurn = `{"model":"opencode-go/deepseek-v4.1-flash","instructions":"You are Codex.",
 "input":[
  {"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"},
   {"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"original"}]},
  {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"I should run ls."}]},
  {"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
  {"type":"function_call","call_id":"call_2","name":"read","namespace":"mcp__files","arguments":"{}"},
  {"type":"function_call_output","call_id":"call_1","output":"a.go\nb.go"},
  {"type":"function_call_output","call_id":"call_2","output":[{"type":"input_text","text":"seen"},
   {"type":"input_image","image_url":"data:image/png;base64,BBBB"}]},
  {"type":"custom_tool_call","call_id":"call_3","name":"apply_patch","input":"*** Begin Patch"},
  {"type":"custom_tool_call_output","call_id":"call_3","output":"Done"},
  {"type":"function_call_output","call_id":"call_gone","output":"an output no call asked for"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Two files."}]}
 ],
 "tools":[
  {"type":"function","name":"exec_command","description":"Runs a command.","parameters":{"type":"object"}},
  {"type":"custom","name":"apply_patch","description":"Edits files.","format":{"type":"grammar"}},
  {"type":"namespace","name":"mcp__files","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]},
  {"type":"web_search"}
 ],
 "tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"High","summary":"auto"},
 "max_output_tokens":2048,"store":false,"include":["reasoning.encrypted_content"],"prompt_cache_key":"conv-1",
 "stream":true}`

// Scenario: the request translated to chat, as CLIProxyAPI's request translator does: the instructions are the
// system message and every role is kept; text alone is one string, an image an image_url at chat's detail; the
// reasoning goes with the tool calls it led to, two calls are one assistant message and their outputs tool
// messages, a tool output's image the user message after them; a freeform tool is a function of one input string and
// a namespace's tool is named under it; a tool chat has no function for is not sent, nor what only the Responses
// API reads (store, include, prompt_cache_key).
func TestARequestIsTranslatedToChat(t *testing.T) {
	t.Parallel()

	body, _ := translate(t, secondTurn)

	want := `{
  "model": "opencode-go/deepseek-v4.1-flash", "stream": true, "max_tokens": 2048, "reasoning_effort": "high",
  "tool_choice": "auto", "parallel_tool_calls": true,
  "messages": [
    {"role": "system", "content": "You are Codex."},
    {"role": "developer", "content": "sandbox: workspace-write"},
    {"role": "user", "content": [{"type": "text", "text": "list the files"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA", "detail": "high"}}]},
    {"role": "assistant", "reasoning_content": "I should run ls.", "tool_calls": [
      {"id": "call_1", "type": "function", "function": {"name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"}},
      {"id": "call_2", "type": "function", "function": {"name": "mcp__files__read", "arguments": "{}"}}]},
    {"role": "tool", "tool_call_id": "call_1", "content": "a.go\nb.go"},
    {"role": "tool", "tool_call_id": "call_2", "content": "seen"},
    {"role": "user", "content": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,BBBB"}}]},
    {"role": "assistant", "reasoning_content": "I should run ls.", "tool_calls": [
      {"id": "call_3", "type": "function",
       "function": {"name": "apply_patch", "arguments": "{\"input\":\"*** Begin Patch\"}"}}]},
    {"role": "tool", "tool_call_id": "call_3", "content": "Done"},
    {"role": "user", "content": "an output no call asked for"},
    {"role": "assistant", "content": "Two files."}
  ],
  "tools": [
    {"type": "function", "function": {"name": "exec_command", "description": "Runs a command.",
      "parameters": {"type": "object"}}},
    {"type": "function", "function": {"name": "apply_patch", "description": "Edits files.",
      "parameters": {"type": "object", "properties": {"input": {"type": "string"}}, "required": ["input"]}}},
    {"type": "function", "function": {"name": "mcp__files__read", "parameters": {"type": "object"}}}
  ]
}`

	assert.JSONEq(t, want, encoded(t, body))
}

// Scenario: a first turn as a string, with no tools: one user message, and no tool fields.
func TestAStringInputIsTheUsersMessage(t *testing.T) {
	t.Parallel()

	body, _ := translate(t, `{"model":"m","input":"hi","tool_choice":"auto","parallel_tool_calls":false}`)

	assert.Equal(t, []any{map[string]any{"role": "user", "content": "hi"}}, body["messages"])

	for _, gone := range []string{"tools", "tool_choice", "parallel_tool_calls", "reasoning_effort", "max_tokens"} {
		assert.NotContains(t, body, gone)
	}
}

// Scenario: a reasoning model's earlier tool call whose reasoning the client did not keep still has some, as
// DeepSeek requires of a tool call's turn (CLIProxyAPI's "[reasoning unavailable]").
func TestAToolCallWithoutItsReasoningKeepsAMarker(t *testing.T) {
	t.Parallel()

	body, _ := translate(t, `{"model":"m","reasoning":{"effort":"medium"},"input":[
  {"type":"message","role":"user","content":"go"},
  {"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},
  {"type":"function_call_output","call_id":"c1","output":"ok"}]}`)

	messages, _ := body["messages"].([]any)
	require.Len(t, messages, 3)
	toolCall, _ := messages[1].(map[string]any)
	assert.Equal(t, "[reasoning unavailable]", toolCall["reasoning_content"])
}

//go:embed testdata/*.sse
var recordedStreams embed.FS

// recorded is a chat stream recorded in testdata: text.sse, a DeepSeek answer of reasoning then text as OpenCode Go
// streams it, its usage in a chunk of its own; tool_call.sse, one that calls Codex's shell, an MCP tool and the
// freeform apply_patch, its arguments in pieces; error.sse, one that ends with an error frame.
func recorded(t *testing.T, name string) string {
	t.Helper()

	data, err := recordedStreams.ReadFile("testdata/" + name)
	require.NoError(t, err)

	return string(data)
}

func encoded(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return string(data)
}

// event is one Responses event as the client reads it: its name and its data.
type event struct {
	name string
	data map[string]any
}

// events reads the framed Responses events a stream made.
func events(t *testing.T, framed string) []event {
	t.Helper()

	var (
		out  []event
		name string
	)

	scanner := bufio.NewScanner(strings.NewReader(framed))
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data), line)
			assert.Equal(t, name, data["type"], "the event's name is its type")
			out = append(out, event{name: name, data: data})
		}
	}

	return out
}

func names(evs []event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.name)
	}

	return out
}

// play feeds a recorded chat stream to a translation, line by line as the gateway reads it, and returns what it
// made, the stream's end included.
func play(t *testing.T, tools *responseschat.Tools, recorded string) (string, *responseschat.Stream) {
	t.Helper()

	stream := responseschat.NewStream("resp_1", 1700000000, "opencode-go/deepseek-v4.1-flash", tools)

	var out strings.Builder

	for line := range strings.Lines(recorded) {
		if payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			out.Write(stream.Chunk([]byte(payload)))
		}
	}

	out.Write(stream.Close())

	return out.String(), stream
}

// Scenario: a recorded text answer becomes the events Codex reads: the response opened, the reasoning as a reasoning
// item's summary, the text as a message, each item closed, then response.completed with both items and the usage
// of the late usage chunk.
func TestATextAnswerIsTranslatedToResponses(t *testing.T) {
	t.Parallel()

	out, stream := play(t, nil, recorded(t, "text.sse"))
	evs := events(t, out)

	assert.Equal(t, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "response.output_item.done",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.completed",
	}, names(evs))
	assert.True(t, stream.Ended())

	for i, e := range evs {
		assert.InDelta(t, float64(i+1), e.data["sequence_number"], 0, "events are numbered in order")
	}

	completed := encoded(t, evs[len(evs)-1].data["response"])
	assert.JSONEq(t, `{"id":"resp_1","object":"response","created_at":1700000000,"status":"completed",
  "model":"opencode-go/deepseek-v4.1-flash",
  "output":[
    {"id":"rs_resp_1_0","type":"reasoning","summary":[{"type":"summary_text","text":"The user greets me."}]},
    {"id":"msg_resp_1_0","type":"message","status":"completed","role":"assistant",
     "content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Hello!"}]}],
  "usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":8},"output_tokens":9,
    "output_tokens_details":{"reasoning_tokens":4},"total_tokens":29}}`, completed)
}

// Scenario: a recorded tool-calling answer becomes Codex's tool calls: a function_call announced as soon as its id
// and name come, its arguments passed on in pieces, closed when the choice finishes; the MCP tool under its own name
// and namespace, the freeform tool a custom_tool_call of its input alone. The next request, made of what Codex keeps
// of this answer, gives the calls back to chat with their reasoning.
func TestAToolCallIsTranslatedBothWays(t *testing.T) {
	t.Parallel()

	_, req := translate(t, secondTurn)

	out, _ := play(t, req.Tools, recorded(t, "tool_call.sse"))
	evs := events(t, out)

	assert.Equal(t, []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "response.output_item.done",
		"response.output_item.added",
		"response.function_call_arguments.delta", "response.function_call_arguments.delta",
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.output_item.added",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.custom_tool_call_input.done", "response.output_item.done",
		"response.completed",
	}, names(evs))

	completed, _ := evs[len(evs)-1].data["response"].(map[string]any)
	assert.JSONEq(t, `[
  {"id":"rs_resp_1_0","type":"reasoning","summary":[{"type":"summary_text","text":"Run ls."}]},
  {"id":"fc_call_a","type":"function_call","status":"completed","arguments":"{\"cmd\":\"ls\"}","call_id":"call_a",
   "name":"exec_command"},
  {"id":"fc_call_b","type":"function_call","status":"completed","arguments":"{}","call_id":"call_b","name":"read",
   "namespace":"mcp__files"},
  {"id":"ctc_call_c","type":"custom_tool_call","status":"completed","input":"*** Begin Patch","call_id":"call_c",
   "name":"apply_patch"}]`, encoded(t, completed["output"]))
	assert.Equal(t, map[string]any{
		"input_tokens": float64(50), "input_tokens_details": map[string]any{"cached_tokens": float64(0)},
		"output_tokens": float64(12), "total_tokens": float64(62),
	}, completed["usage"])

	// What Codex sends next: the items it was given, then each call's output.
	items, _ := completed["output"].([]any)
	items = append(items,
		map[string]any{"type": "function_call_output", "call_id": "call_a", "output": "a.go"},
		map[string]any{"type": "function_call_output", "call_id": "call_b", "output": "{}"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_c", "output": "Done"})

	var next map[string]any
	require.NoError(t, json.Unmarshal([]byte(secondTurn), &next))
	next["input"] = items

	body, _ := translate(t, encoded(t, next))
	messages, _ := body["messages"].([]any)
	require.Len(t, messages, 5, "the system message, one assistant message of three calls, three tool messages")

	assert.JSONEq(t, `{"role":"assistant","reasoning_content":"Run ls.","tool_calls":[
  {"id":"call_a","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}},
  {"id":"call_b","type":"function","function":{"name":"mcp__files__read","arguments":"{}"}},
  {"id":"call_c","type":"function","function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}}]}`,
		encoded(t, messages[1]))
}

// Scenario: a provider that ends its stream with an error frame: the answer ends with response.failed, its code
// and message, and nothing after it is translated.
func TestAnErrorFrameIsTranslatedToAFailure(t *testing.T) {
	t.Parallel()

	out, stream := play(t, nil, recorded(t, "error.sse"))
	evs := events(t, out)

	require.NotEmpty(t, evs)
	assert.True(t, stream.Ended())

	last := evs[len(evs)-1]
	assert.Equal(t, "response.failed", last.name)
	response, _ := last.data["response"].(map[string]any)
	assert.Equal(t, map[string]any{"code": "rate_limit_exceeded", "message": "Rate limit reached for deepseek"},
		response["error"])
	assert.NotContains(t, out, " again")
}

// Scenario: a stream that stops before its choice finished is left without its end, so the client and the gateway
// read it as cut; one that finished but sent no [DONE] is completed.
func TestAStreamCutBeforeItsFinishHasNoEnd(t *testing.T) {
	t.Parallel()

	out, stream := play(t, nil,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{\"a"}}]}}]}`)
	assert.False(t, stream.Ended())
	assert.NotContains(t, out, "response.completed")
	assert.NotContains(t, out, "response.output_item.done")

	out, stream = play(t, nil, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	assert.True(t, stream.Ended())
	assert.Contains(t, out, "response.completed")
}

// Scenario: an answer cut at its token limit ends with response.incomplete and says why.
func TestAnAnswerCutAtItsLimitIsIncomplete(t *testing.T) {
	t.Parallel()

	out, _ := play(t, nil, `data: {"choices":[{"index":0,"delta":{"content":"long"},"finish_reason":"length"}]}

data: [DONE]
`)
	evs := events(t, out)
	last := evs[len(evs)-1]
	assert.Equal(t, "response.incomplete", last.name)

	response, _ := last.data["response"].(map[string]any)
	assert.Equal(t, "incomplete", response["status"])
	assert.Equal(t, map[string]any{"reason": "max_output_tokens"}, response["incomplete_details"])
}

// Scenario: a provider that answers a stream with a whole completion: it is translated as one chunk and ended.
func TestAWholeCompletionIsTranslated(t *testing.T) {
	t.Parallel()

	stream := responseschat.NewStream("resp_2", 1, "m", nil)
	evs := events(t, string(stream.Completion([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,
  "message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_z","type":"function",
  "function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,
  "completion_tokens":1,"total_tokens":4}}`))))

	require.NotEmpty(t, evs)
	assert.Equal(t, "response.completed", evs[len(evs)-1].name)
	assert.Contains(t, names(evs), "response.function_call_arguments.done")
}
