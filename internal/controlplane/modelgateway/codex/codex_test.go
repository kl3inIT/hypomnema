package codex_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
)

func build(t *testing.T, model, chat string) (codex.Request, map[string]any) {
	t.Helper()

	req, err := codex.BuildRequest([]byte(chat), codex.Options{Model: model, CacheKey: "key-of-the-gateway-key"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &body))

	return req, body
}

// A conversation with a system prompt, a tool call already made and its
// result, as pi sends it on the second turn.
const conversation = `{
  "model": "anything", "stream": false, "temperature": 0.2, "max_tokens": 4000, "user": "u1",
  "metadata": {"a": "b"}, "stream_options": {"include_usage": true}, "reasoning_effort": "high",
  "service_tier": "fast",
  "messages": [
    {"role": "system", "content": "You plan work."},
    {"role": "developer", "content": [{"type": "text", "text": "Answer briefly."}]},
    {"role": "user", "content": [{"type": "text", "text": "What is in the repo?"},
                                 {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}]},
    {"role": "assistant", "content": "I will look.", "tool_calls": [
      {"id": "call_1", "type": "function",
       "function": {"name": "fs.read file", "arguments": "{\"path\":\"README.md\"}"}}]},
    {"role": "tool", "tool_call_id": "call_1", "content": [{"type": "text", "text": "# Hypomnema"},
                                                         {"type": "text", "text": "A control plane."}]},
    {"role": "user", "content": "Thanks."}
  ],
  "tools": [{"type": "function", "function": {"name": "fs.read file", "description": "Read a file",
    "parameters": {"type": "object", "properties": {"path": {"type": "string", "pattern": "^\\p{L}+$"}}}}}],
  "tool_choice": {"type": "function", "function": {"name": "fs.read file"}},
  "response_format": {"type": "json_schema", "json_schema": {"name": "answer", "schema": {"type": "object"}}}
}`

// Scenario: a chat request for a classic model becomes the Responses body
// the backend takes. Instructions come from the system and developer
// messages, the tool call and its result become items of their own, the tool
// gets a name the backend accepts, and what the backend refuses is left out.
func TestAChatRequestBecomesTheBodyTheBackendTakes(t *testing.T) {
	t.Parallel()

	req, body := build(t, "gpt-5.6-sol", conversation)

	assert.False(t, req.Lite)
	assert.Equal(t, "gpt-5.6-sol", body["model"])
	assert.Equal(t, true, body["stream"], "the backend only streams")
	assert.Equal(t, false, body["store"])
	assert.Equal(t, "You plan work.\n\nAnswer briefly.", body["instructions"])
	assert.Equal(t, map[string]any{"effort": "high", "summary": "auto"}, body["reasoning"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	assert.Equal(t, "priority", body["service_tier"])
	assert.Equal(t, "key-of-the-gateway-key", body["prompt_cache_key"])
	assert.Equal(t, map[string]any{"type": "function", "name": "fs_read_file"}, body["tool_choice"])
	assert.Equal(t, map[string]any{"format": map[string]any{
		"type": "json_schema", "name": "answer", "strict": true, "schema": map[string]any{"type": "object"},
	}}, body["text"])

	for _, refused := range []string{"temperature", "max_tokens", "max_output_tokens", "user", "metadata",
		"stream_options", "messages", "reasoning_effort", "response_format"} {
		assert.NotContains(t, body, refused)
	}

	assert.Equal(t, []any{
		map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "What is in the repo?"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
		}},
		map[string]any{"type": "message", "role": "assistant", "content": []any{
			map[string]any{"type": "output_text", "text": "I will look."},
		}},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "fs_read_file",
			"arguments": `{"path":"README.md"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "# Hypomnema\nA control plane."},
		map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "Thanks."},
		}},
	}, body["input"])

	assert.Equal(t, []any{map[string]any{
		"type": "function", "name": "fs_read_file", "description": "Read a file", "strict": false,
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string"},
		}},
	}}, body["tools"], "a pattern with a Unicode property is refused by the backend and is dropped")

	assert.Equal(t, "fs.read file", req.Names.Original("fs_read_file"))
	assert.Equal(t, "other", req.Names.Original("other"))
}

// Scenario: the same request for a gpt-6 model takes the lite shape, as the
// Codex CLI sends it: tools and instructions at the head of the input, no
// top-level tools, one tool call at a time, reasoning over every turn with
// no summary.
func TestAGPT6ModelTakesTheLiteShape(t *testing.T) {
	t.Parallel()

	req, body := build(t, "gpt-6.1-sol", conversation)

	assert.True(t, req.Lite)
	assert.Empty(t, body["instructions"])
	assert.Contains(t, body, "tools")
	assert.Nil(t, body["tools"])
	assert.Equal(t, false, body["parallel_tool_calls"])
	assert.Equal(t, map[string]any{"effort": "high", "context": "all_turns"}, body["reasoning"])

	input, ok := body["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 7)

	tools, ok := input[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "additional_tools", tools["type"])
	assert.Equal(t, "developer", tools["role"])
	assert.Len(t, tools["tools"], 1)

	assert.Equal(t, map[string]any{"type": "message", "role": "developer", "content": []any{
		map[string]any{"type": "input_text", "text": "You plan work.\n\nAnswer briefly."},
	}}, input[1])

	_, plain := build(t, "gpt-6.1-sol", `{"messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, map[string]any{"effort": "medium", "context": "all_turns"}, plain["reasoning"],
		"a lite model reasons at medium when the client names no effort")
	assert.Equal(t, "auto", plain["tool_choice"])
	assert.Len(t, plain["input"], 1, "with no tools and no instructions nothing is put before the conversation")
}

// Scenario: tool names and call ids the backend would refuse. Two long names
// that shorten to the same thing stay apart; a call id over 64 bytes is cut
// the same way in the call and in its result; a classic model with no effort
// gets no reasoning block.
func TestNamesAndIDsAreMadeToFitTheBackend(t *testing.T) {
	t.Parallel()

	long := "mcp__" + strings.Repeat("server", 12) + "__read"
	twin := "mcp__" + strings.Repeat("other_", 12) + "__read"
	id := "call_" + strings.Repeat("x", 80)

	req, body := build(t, "gpt-5.6-sol", `{"messages":[
	  {"role":"assistant","tool_calls":[{"id":"`+id+`","function":{"name":"`+long+`","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"`+id+`","content":"done"}],
	  "tools":[{"type":"function","function":{"name":"`+long+`"}},{"type":"function","function":{"name":"`+twin+`"}}]}`)

	assert.NotContains(t, body, "reasoning")
	assert.NotContains(t, body, "include")

	input, ok := body["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	call, _ := input[0].(map[string]any)
	result, _ := input[1].(map[string]any)
	assert.Equal(t, "mcp__read", call["name"])
	assert.Len(t, call["call_id"], 64)
	assert.Equal(t, call["call_id"], result["call_id"])

	assert.Equal(t, long, req.Names.Original("mcp__read"))
	assert.Equal(t, twin, req.Names.Original("mcp__read_1"))
}

// feed gives a stream the events of one answer and returns the chunks.
func feed(s *codex.Stream, events ...string) []map[string]any {
	var chunks []map[string]any

	for _, ev := range events {
		for _, raw := range s.Event([]byte(ev)) {
			var chunk map[string]any

			_ = json.Unmarshal(raw, &chunk)
			chunks = append(chunks, chunk)
		}
	}

	return chunks
}

func delta(chunk map[string]any) map[string]any {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}

	choice, _ := choices[0].(map[string]any)
	d, _ := choice["delta"].(map[string]any)

	return d
}

func finish(chunk map[string]any) any {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}

	choice, _ := choices[0].(map[string]any)

	return choice["finish_reason"]
}

// Scenario: the backend thinks, then answers in text. The client gets the
// reasoning summary and the text as they come, then the finish reason, then
// the usage in a chunk of its own; a client that did not stream gets the
// same as one completion.
func TestAnAnswerInTextStreamsAsChatChunks(t *testing.T) {
	t.Parallel()

	s := codex.NewStream("chatcmpl-1", 1794290000, "gpt-sol", nil)
	chunks := feed(s,
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"Thinking."}`,
		`{"type":"response.reasoning_summary_text.done"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","delta":"Hel"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","delta":"lo"}`,
		`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message",`+
			`"content":[{"type":"output_text","text":"Hello"}]}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":30,"output_tokens":7,"total_tokens":37,`+
			`"input_tokens_details":{"cached_tokens":20},"output_tokens_details":{"reasoning_tokens":4}}}}`,
		`{"type":"response.output_text.delta","delta":"ignored after the end"}`)

	require.Len(t, chunks, 6)
	assert.Equal(t, map[string]any{"role": "assistant", "reasoning_content": "Thinking."}, delta(chunks[0]))
	assert.Equal(t, map[string]any{"reasoning_content": "\n\n"}, delta(chunks[1]))
	assert.Equal(t, map[string]any{"content": "Hel"}, delta(chunks[2]))
	assert.Equal(t, map[string]any{"content": "lo"}, delta(chunks[3]))
	assert.Equal(t, "stop", finish(chunks[4]))
	assert.Empty(t, chunks[5]["choices"])
	assert.Equal(t, map[string]any{
		"prompt_tokens": 30.0, "completion_tokens": 7.0, "total_tokens": 37.0,
		"prompt_tokens_details":     map[string]any{"cached_tokens": 20.0},
		"completion_tokens_details": map[string]any{"reasoning_tokens": 4.0},
	}, chunks[5]["usage"])

	for _, chunk := range chunks {
		assert.Equal(t, "chatcmpl-1", chunk["id"])
		assert.Equal(t, "chat.completion.chunk", chunk["object"])
		assert.Equal(t, "gpt-sol", chunk["model"])
	}

	require.True(t, s.Finished())
	assert.JSONEq(t, `{"id":"chatcmpl-1","object":"chat.completion","created":1794290000,"model":"gpt-sol",
	  "choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Hello",
	    "reasoning_content":"Thinking."}}],
	  "usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37,
	    "prompt_tokens_details":{"cached_tokens":20},"completion_tokens_details":{"reasoning_tokens":4}}}`,
		string(s.Completion()))
}

// Scenario: the backend calls two tools. Each call opens with its index, id
// and the client's own name for the tool; arguments arrive in pieces for the
// first and only with the finished item for the second; nothing is sent
// twice when the backend repeats the whole arguments at the end.
func TestToolCallsKeepTheirIndexAndSendArgumentsOnce(t *testing.T) {
	t.Parallel()

	s := codex.NewStream("chatcmpl-2", 1, "gpt-sol", codex.Names{"fs_read": "fs.read"})
	chunks := feed(s,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"fs_read"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"a\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"path\":\"a\"}"}`,
		`{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_a",`+
			`"name":"fs_read","arguments":"{\"path\":\"a\"}"}}`,
		`{"type":"response.output_item.done","item":{"id":"fc_2","type":"function_call","call_id":"call_b",`+
			`"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"response.completed","response":{}}`)

	require.Len(t, chunks, 6)
	assert.Equal(t, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
		"index": 0.0, "id": "call_a", "type": "function", "function": map[string]any{"name": "fs.read", "arguments": ""},
	}}}, delta(chunks[0]))
	assert.Equal(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 0.0, "function": map[string]any{"arguments": `{"path":`},
	}}}, delta(chunks[1]))
	assert.Equal(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 1.0, "id": "call_b", "type": "function", "function": map[string]any{"name": "shell", "arguments": ""},
	}}}, delta(chunks[3]))
	assert.Equal(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 1.0, "function": map[string]any{"arguments": `{"cmd":"ls"}`},
	}}}, delta(chunks[4]))
	assert.Equal(t, "tool_calls", finish(chunks[5]))

	assert.JSONEq(t, `{"id":"chatcmpl-2","object":"chat.completion","created":1,"model":"gpt-sol","choices":[{"index":0,
	  "finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
	    {"id":"call_a","type":"function","function":{"name":"fs.read","arguments":"{\"path\":\"a\"}"}},
	    {"id":"call_b","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		string(s.Completion()))
}

// Scenario: answers that do not end well. Text that came only with its
// finished item is still delivered; an answer cut at the token limit says
// "length"; an error inside the stream gives no chunk and is reported with
// the status it stands for and the time a usage limit ends.
func TestAnAnswerThatIsCutOrFails(t *testing.T) {
	t.Parallel()

	cut := codex.NewStream("c", 1, "m", nil)
	chunks := feed(cut,
		`{"type":"response.output_item.done","item":{"id":"msg_9","type":"message",`+
			`"content":[{"type":"output_text","text":"Half an ans"}]}}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`)

	require.Len(t, chunks, 2)
	assert.Equal(t, "Half an ans", delta(chunks[0])["content"])
	assert.Equal(t, "length", finish(chunks[1]))

	failed := codex.NewStream("c", 1, "m", nil)
	assert.Empty(t, feed(failed, `{"type":"response.failed","response":{"error":`+
		`{"type":"usage_limit_reached","message":"limit","resets_at":1794300000}}}`,
		`{"type":"response.output_text.delta","delta":"never sent"}`))
	assert.False(t, failed.Finished())

	failure := failed.Failed()
	require.NotNil(t, failure)
	assert.Equal(t, http.StatusTooManyRequests, failure.Status)
	assert.Equal(t, "usage_limit_reached", failure.Type)
	assert.Equal(t, time.Unix(1794300000, 0), failure.ResetsAt)

	plain := codex.NewStream("c", 1, "m", nil)
	feed(plain, `{"type":"error","code":"server_error","message":"The model is overloaded"}`)
	require.NotNil(t, plain.Failed())
	assert.Equal(t, http.StatusBadGateway, plain.Failed().Status)
	assert.Equal(t, "The model is overloaded", plain.Failed().Message)

	refused := codex.ParseFailure(http.StatusBadRequest,
		[]byte(`{"error":{"type":"usage_limit_reached","message":"You hit your limit","resets_in_seconds":3600}}`))
	assert.Equal(t, http.StatusTooManyRequests, refused.Status)
	assert.WithinDuration(t, time.Now().Add(time.Hour), refused.ResetsAt, time.Minute)

	unsupported := codex.ParseFailure(http.StatusBadRequest, []byte(`{"detail":"Unsupported parameter: temperature"}`))
	assert.Equal(t, http.StatusBadRequest, unsupported.Status)
}
