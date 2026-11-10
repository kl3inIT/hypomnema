package antigravity_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/antigravity"
)

func build(t *testing.T, model, chat string) (antigravity.Request, map[string]any, map[string]any) {
	t.Helper()

	req, err := antigravity.BuildRequest([]byte(chat), antigravity.Options{
		Model: model, Project: "bright-wave-1234", RequestID: "agent-1",
	})
	require.NoError(t, err)

	var envelope map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &envelope))

	request, ok := envelope["request"].(map[string]any)
	require.True(t, ok)

	return req, envelope, request
}

// A second turn of a conversation with a tool: two calls in one assistant
// turn, their results, and tools whose schemas hold what Gemini refuses.
const conversation = `{
  "model": "anything", "stream": true, "reasoning_effort": "high", "temperature": 0.3, "max_tokens": 200000,
  "messages": [
    {"role": "system", "content": "You plan work."},
    {"role": "user", "content": [{"type": "text", "text": "What is in the repo?"},
                                 {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}]},
    {"role": "assistant", "content": "I will look.", "tool_calls": [
      {"id": "call_1", "type": "function", "function": {"name": "fs.read", "arguments": "{\"path\":\"README.md\"}"}},
      {"id": "call_2", "type": "function", "function": {"name": "list", "arguments": ""}}]},
    {"role": "tool", "tool_call_id": "call_2", "content": "a.go"},
    {"role": "tool", "tool_call_id": "call_1", "content": [{"type": "text", "text": "# Hypomnema"}]},
    {"role": "assistant", "content": "Done."}
  ],
  "tools": [
    {"type": "function", "function": {"name": "fs.read", "description": "Read a file", "parameters": {
      "$schema": "http://json-schema.org/draft-07/schema#", "type": "object", "additionalProperties": false,
      "required": ["path", "ghost"],
      "properties": {
        "path": {"type": ["string", "null"], "format": "uri", "default": "x", "minLength": "1"},
        "mode": {"anyOf": [{"type": "null"}, {"const": "r", "enum": ["r", 2]}]},
        "tags": {"type": "array"}}}}},
    {"type": "function", "function": {"name": "list"}}
  ]
}`

// Scenario: a chat request becomes the envelope the backend takes. The
// instruction is a turn of the user's; the assistant's two calls are parts of
// one model turn, the first with the word that stands for a thought
// signature; their results follow in one user turn, each with its call's id
// and name; a conversation that ends with the assistant gets a user turn.
// Nothing is added to look like the IDE, and no requestType is sent.
func TestAChatRequestBecomesTheEnvelopeTheBackendTakes(t *testing.T) {
	t.Parallel()

	req, envelope, request := build(t, "gemini-3.8-flash-high", conversation)

	assert.Equal(t, "bright-wave-1234", envelope["project"])
	assert.Equal(t, "gemini-3.8-flash-high", envelope["model"])
	assert.Equal(t, "antigravity", envelope["userAgent"])
	assert.Equal(t, "agent-1", envelope["requestId"])
	assert.NotContains(t, envelope, "requestType")
	assert.Regexp(t, `^-\d+$`, request["sessionId"])

	assert.Equal(t, map[string]any{"role": "user", "parts": []any{map[string]any{"text": "You plan work."}}},
		request["systemInstruction"])

	assert.Equal(t, []any{
		map[string]any{"role": "user", "parts": []any{
			map[string]any{"text": "What is in the repo?"},
			map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "AAAA"}},
		}},
		map[string]any{"role": "model", "parts": []any{
			map[string]any{"text": "I will look."},
			map[string]any{
				"functionCall":     map[string]any{"id": "call_1", "name": "fs_read", "args": map[string]any{"path": "README.md"}},
				"thoughtSignature": antigravity.SkipSignature,
			},
			map[string]any{"functionCall": map[string]any{"id": "call_2", "name": "list", "args": map[string]any{}}},
		}},
		map[string]any{"role": "user", "parts": []any{
			map[string]any{"functionResponse": map[string]any{
				"id": "call_2", "name": "list", "response": map[string]any{"result": "a.go"},
			}},
			map[string]any{"functionResponse": map[string]any{
				"id": "call_1", "name": "fs_read", "response": map[string]any{"result": "# Hypomnema"},
			}},
		}},
		map[string]any{"role": "model", "parts": []any{map[string]any{"text": "Done."}}},
		map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Continue."}}},
	}, request["contents"])

	assert.Equal(t, map[string]any{"functionCallingConfig": map[string]any{"mode": "VALIDATED"}}, request["toolConfig"])
	assert.Equal(t, map[string]any{
		"temperature": 0.3, "maxOutputTokens": 64000.0,
		"thinkingConfig": map[string]any{"thinkingLevel": "high", "includeThoughts": true},
	}, request["generationConfig"])

	tools, ok := request["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)

	names := []any{}
	for _, declaration := range declarations(t, request) {
		names = append(names, declaration["name"])
	}

	assert.Equal(t, []any{"fs_read", "list"}, names, "the client's tools and no other")
	assert.Equal(t, "fs.read", req.Names.Original("fs_read"))
	assert.NotContains(t, string(req.Body), "You are Antigravity")
}

// Scenario: tool schemas with what Gemini refuses. Unknown keywords go, a
// union keeps its first real branch, a type list keeps its first real type,
// an enum becomes strings, a limit written as text becomes a number, a
// required name with no property goes, an array gets items, and a tool with
// no parameter gets the one a model in VALIDATED mode needs.
func TestToolSchemasAreCleanedForGemini(t *testing.T) {
	t.Parallel()

	_, _, request := build(t, "claude-sonnet-5-5-high", conversation)
	declared := declarations(t, request)

	assert.Equal(t, map[string]any{
		"name": "fs_read", "description": "Read a file",
		"parameters": map[string]any{
			"type": "object", "required": []any{"path"},
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "minLength": 1.0},
				"mode": map[string]any{"type": "string", "enum": []any{"r", "2"}},
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	}, declared[0])

	assert.Equal(t, map[string]any{
		"name": "list",
		"parameters": map[string]any{
			"type": "object", "required": []any{"reason"},
			"properties": map[string]any{"reason": map[string]any{
				"type": "string", "description": "Brief explanation of why you are calling this tool",
			}},
		},
	}, declared[1])

	config, _ := request["generationConfig"].(map[string]any)
	assert.NotContains(t, config, "thinkingConfig", "a Claude model's effort is in its name at the backend")
}

// Scenario: what the client chooses about tools. "none" sends no tool at
// all; "required" and a named function make the model call one; a
// conversation that starts with the assistant gets a user turn first.
func TestToolChoiceAndAConversationThatStartsWithTheAssistant(t *testing.T) {
	t.Parallel()

	tools := `"tools":[{"type":"function","function":{"name":"1st tool!"}}]`

	_, _, none := build(t, "gemini-3.8-flash-high",
		`{"messages":[{"role":"user","content":"hi"}],"tool_choice":"none",`+tools+`}`)
	assert.NotContains(t, none, "tools")
	assert.NotContains(t, none, "toolConfig")
	assert.NotContains(t, none, "generationConfig")

	_, _, required := build(t, "gemini-3.8-flash-high",
		`{"messages":[{"role":"assistant","content":"Hello."}],"tool_choice":"required",`+tools+`}`)
	assert.Equal(t, map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY"}}, required["toolConfig"])

	contents, _ := required["contents"].([]any)
	require.Len(t, contents, 3)
	assert.Equal(t, "user", contents[0].(map[string]any)["role"]) //nolint:forcetypeassert // Built here.

	req, _, named := build(t, "gemini-3.8-flash-high", `{"messages":[{"role":"user","content":"hi"}],`+
		`"tool_choice":{"type":"function","function":{"name":"1st tool!"}},`+tools+`}`)
	assert.Equal(t, map[string]any{"functionCallingConfig": map[string]any{
		"mode": "ANY", "allowedFunctionNames": []any{"_1st_tool_"},
	}}, named["toolConfig"])
	assert.Equal(t, "1st tool!", req.Names.Original("_1st_tool_"))
}

// declarations are the functions a built request declares.
func declarations(t *testing.T, request map[string]any) []map[string]any {
	t.Helper()

	tools, ok := request["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)

	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)

	list, ok := tool["functionDeclarations"].([]any)
	require.True(t, ok)

	out := make([]map[string]any, 0, len(list))

	for _, raw := range list {
		declaration, ok := raw.(map[string]any)
		require.True(t, ok)

		out = append(out, declaration)
	}

	return out
}

func feed(s *antigravity.Stream, events ...string) []map[string]any {
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

func choice(chunk map[string]any) map[string]any {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}

	c, _ := choices[0].(map[string]any)

	return c
}

// Scenario: the backend thinks, answers in text and calls a tool. The client
// gets the thought as reasoning, the text, the call whole with the client's
// own name for the tool, then the finish reason and the usage, which counts
// the thought tokens as output.
func TestAnAnswerStreamsAsChatChunks(t *testing.T) {
	t.Parallel()

	s := antigravity.NewStream("chatcmpl-1", 1794290000, "flash", antigravity.Names{"fs_read": "fs.read"})
	chunks := feed(s,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"Thinking."}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"Hel"},{"text":"lo"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"c2ln"},`+
			`{"functionCall":{"name":"fs_read","args":{"path":"a"}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":7,"thoughtsTokenCount":4,`+
			`"cachedContentTokenCount":20,"totalTokenCount":41}}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"ignored after the end"}]}}]}}`)

	require.Len(t, chunks, 6)
	assert.Equal(t, map[string]any{"role": "assistant", "reasoning_content": "Thinking."}, choice(chunks[0])["delta"])
	assert.Equal(t, map[string]any{"content": "Hel"}, choice(chunks[1])["delta"])
	assert.Equal(t, map[string]any{"content": "lo"}, choice(chunks[2])["delta"])
	assert.Equal(t, map[string]any{"tool_calls": []any{map[string]any{
		"index": 0.0, "id": "call_chatcmpl-1_0", "type": "function",
		"function": map[string]any{"name": "fs.read", "arguments": `{"path":"a"}`},
	}}}, choice(chunks[3])["delta"])
	assert.Equal(t, "tool_calls", choice(chunks[4])["finish_reason"])
	assert.Equal(t, map[string]any{
		"prompt_tokens": 30.0, "completion_tokens": 11.0, "total_tokens": 41.0,
		"prompt_tokens_details":     map[string]any{"cached_tokens": 20.0},
		"completion_tokens_details": map[string]any{"reasoning_tokens": 4.0},
	}, chunks[5]["usage"])

	require.True(t, s.Finished())
	assert.JSONEq(t, `{"id":"chatcmpl-1","object":"chat.completion","created":1794290000,"model":"flash","choices":[{
	  "index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"Hello",
	  "reasoning_content":"Thinking.","tool_calls":[{"id":"call_chatcmpl-1_0","type":"function",
	  "function":{"name":"fs.read","arguments":"{\"path\":\"a\"}"}}]}}],
	  "usage":{"prompt_tokens":30,"completion_tokens":11,"total_tokens":41,
	  "prompt_tokens_details":{"cached_tokens":20},"completion_tokens_details":{"reasoning_tokens":4}}}`,
		string(s.Completion()))

	cut := antigravity.NewStream("c", 1, "m", nil)
	chunks = feed(cut, `{"response":{"candidates":[{"content":{"parts":[{"text":"Half"}]},"finishReason":"MAX_TOKENS"}]}}`)
	require.Len(t, chunks, 2)
	assert.Equal(t, "length", choice(chunks[1])["finish_reason"])
}

// Scenario: the backend refuses, and says why in Google's way. A daily quota
// that is used up, a short rate limit with the wait to observe, a suspended
// account with its appeal link and an account to verify are told apart; an
// error inside a stream gives no chunk.
func TestTheBackendsRefusalsAreToldApart(t *testing.T) {
	t.Parallel()

	quota := antigravity.ParseFailure(http.StatusTooManyRequests, []byte(`{"error":{"code":429,
	  "status":"RESOURCE_EXHAUSTED","message":"You have exhausted your capacity on this model.","details":[
	  {"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED",
	   "domain":"cloudcode-pa.googleapis.com",
	   "metadata":{"quotaResetDelay":"1h12m28.5s","model":"gemini-4-argon-high"}},
	  {"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34.07s"}]}}`))
	assert.Equal(t, http.StatusTooManyRequests, quota.Status)
	assert.Equal(t, antigravity.ReasonQuotaExhausted, quota.Reason)
	assert.Equal(t, "resource_exhausted", quota.Code)
	assert.Equal(t, time.Hour+12*time.Minute+28500*time.Millisecond, quota.RetryAfter)

	rate := antigravity.ParseFailure(http.StatusTooManyRequests, []byte(
		`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"},{"retryDelay":"3s"}]}}`))
	assert.Equal(t, antigravity.ReasonRateLimited, rate.Reason)
	assert.Equal(t, 3*time.Second, rate.RetryAfter)

	suspended := antigravity.ParseFailure(http.StatusForbidden, []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED",
	  "message":"This service has been disabled in this account for violation of Terms of Service.","details":[
	  {"reason":"TOS_VIOLATION","metadata":{"appeal_url":"https://support.google.com/appeal"}}]}}`))
	assert.Equal(t, antigravity.ReasonTermsViolation, suspended.Reason)
	assert.Equal(t, "https://support.google.com/appeal", suspended.URL)

	plain := antigravity.ParseFailure(http.StatusForbidden, []byte(
		`{"error":{"message":"Gemini has been disabled in this account for violation of the terms."}}`))
	assert.Equal(t, antigravity.ReasonTermsViolation, plain.Reason)

	verify := antigravity.ParseFailure(http.StatusForbidden, []byte(`{"error":{"status":"PERMISSION_DENIED","details":[
	  {"reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/verify"}}]}}`))
	assert.Equal(t, antigravity.ReasonVerification, verify.Reason)
	assert.Equal(t, "https://accounts.google.com/verify", verify.URL)

	assert.Equal(t, http.StatusBadGateway, antigravity.ParseFailure(http.StatusBadGateway, []byte("<html>")).Status)

	s := antigravity.NewStream("c", 1, "m", nil)
	assert.Empty(t, feed(s, `{"error":{"code":503,"status":"UNAVAILABLE","message":"The model is overloaded.",`+
		`"details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"never sent"}]}}]}}`))
	require.NotNil(t, s.Failed())
	assert.Equal(t, http.StatusServiceUnavailable, s.Failed().Status)
	assert.Equal(t, antigravity.ReasonNoCapacity, s.Failed().Reason)
	assert.Contains(t, s.Failed().Message, "overloaded")
}
