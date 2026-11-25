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

// chatToolCall is a chat model's answer that calls Codex's shell, recorded as OpenCode Go streams a DeepSeek model:
// reasoning, the call in pieces, the finish, a usage chunk and the end.
var chatToolCall = []string{
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant",` +
		`"reasoning_content":"List the files."}}]}` + "\n\n",
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,` +
		`"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":"}}]}}]}` + "\n\n",
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,` +
		`"function":{"arguments":"\"ls\"}"}}]}}]}` + "\n\n",
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},` +
		`"finish_reason":"tool_calls"}]}` + "\n\n",
	`data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":40,` +
		`"completion_tokens":7,"total_tokens":47,"prompt_tokens_details":{"cached_tokens":16}}}` + "\n\n",
	"data: [DONE]\n\n",
}

// What the Codex CLI sends a model it does not bundle: its instructions, the user's turn and its shell as a tool.
const codexAsksAChatModel = `{"model":"%s","instructions":"You are Codex.","input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]}],` +
	`"tools":[{"type":"function","name":"exec_command","description":"Runs a command.",` +
	`"parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],` +
	`"tool_choice":"auto","parallel_tool_calls":false,"reasoning":{"effort":"high","summary":"auto"},` +
	`"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"conv-1"}`

// completedOf is the response of the stream's response.completed event.
func completedOf(t *testing.T, body string) map[string]any {
	t.Helper()

	for line := range strings.Lines(body) {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || !strings.Contains(payload, `"response.completed"`) {
			continue
		}

		var ev struct {
			Type     string         `json:"type"`
			Response map[string]any `json:"response"`
		}

		require.NoError(t, json.Unmarshal([]byte(payload), &ev))

		if ev.Type == "response.completed" {
			return ev.Response
		}
	}

	require.Failf(t, "no response.completed", "%s", body)

	return nil
}

// Scenario (issue #1144): the Codex CLI calls an OpenCode Go model Go serves on chat, DeepSeek's. The request goes
// to Go's chat path in chat's format, under the model's name at Go, the conversation's session and a Go key; the
// chat stream comes back as the Responses events Codex reads, its tool call a function_call; the usage of the
// stream's usage chunk is recorded.
func TestACodexClientIsServedAGoChatModel(t *testing.T) {
	t.Parallel()

	backend := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			stream(chatToolCall...)(w, r)
		case "/openai/v1/responses":
			stream(helloEvents...)(w, r)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	g := newGoGateway(t, backend, &accounts{})

	status, headers, body := g.respond(t, g.key, strings.ReplaceAll(codexAsksAChatModel, "%s", "opencode-go/glm-5.3"),
		"session-id", "sess-1")
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))
	assert.Equal(t, "/openai/v1/chat/completions", backend.lastPath(t))

	sentHeaders, sent := backend.got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	assert.JSONEq(t, `{"model":"glm-5.3","stream":true,"stream_options":{"include_usage":true},
  "messages":[{"role":"system","content":"You are Codex."},{"role":"user","content":"list the files"}],
  "tools":[{"type":"function","function":{"name":"exec_command","description":"Runs a command.",
    "parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}}],
  "tool_choice":"auto","parallel_tool_calls":false,"reasoning_effort":"high"}`, sent)

	response := completedOf(t, body)
	assert.Equal(t, "opencode-go/glm-5.3", response["model"])

	id, _ := response["id"].(string)
	output, err := json.Marshal(response["output"])
	require.NoError(t, err)
	assert.JSONEq(t, `[
  {"id":"rs_`+id+`_0","type":"reasoning","summary":[{"type":"summary_text","text":"List the files."}]},
  {"id":"fc_call_1","type":"function_call","status":"completed","arguments":"{\"cmd\":\"ls\"}","call_id":"call_1",
   "name":"exec_command"}]`, string(output))

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome, u.Error)
	assert.Equal(t, "opencode-go/glm-5.3", u.Model)
	assert.Equal(t, "opencode-go", u.Provider)
	assert.Equal(t, "glm-5.3", u.UpstreamModel)
	assert.Equal(t, http.StatusOK, u.Status)
	assert.Equal(t, int64(40), u.Tokens.Prompt)
	assert.Equal(t, int64(7), u.Tokens.Completion)
	assert.Equal(t, int64(16), u.Tokens.Cached)

	// The same conversation on a Go model served on Responses is sent the same Go session.
	chatSession := sentHeaders.Get("X-Opencode-Session")
	require.NotEmpty(t, chatSession)

	status, _, body = g.respond(t, g.key, `{"model":"opencode-go/gpt-6-luna","input":"hi"}`, "session-id", "sess-1")
	require.Equal(t, http.StatusOK, status, body)

	passedHeaders, _ := backend.got.last(t)
	assert.Equal(t, chatSession, passedHeaders.Get("X-Opencode-Session"))
}

// Scenario (issue #1144): the Codex CLI calls a model of an OpenAI-compatible provider. The provider is asked in
// chat's format with its own key, and its text answer comes back as a Responses message; a refusal comes back with
// the provider's status and body; an error frame inside the stream ends it with response.failed, and the record
// says why.
func TestACodexClientIsServedAnOpenAICompatibleModel(t *testing.T) {
	t.Parallel()

	hello := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hel"}}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n",
		"data: [DONE]\n\n",
	}

	t.Run("an answer", func(t *testing.T) {
		t.Parallel()

		upstream, got := provider(t, stream(hello...))
		g := newChatGateway(t, modelgateway.Config{}, upstream)

		status, _, body := g.respond(t, g.key, `{"model":"gpt","input":"hi"}`)
		require.Equal(t, http.StatusOK, status, body)

		headers, sent := got.last(t)
		assert.Equal(t, "Bearer sk-of-vault:p1", headers.Get("Authorization"))
		assert.JSONEq(t, `{"model":"up-1","stream":true,"stream_options":{"include_usage":true},`+
			`"messages":[{"role":"user","content":"hi"}]}`, sent)

		response := completedOf(t, body)
		output, _ := response["output"].([]any)
		require.Len(t, output, 1)
		message, _ := output[0].(map[string]any)
		assert.Equal(t, []any{map[string]any{
			"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": "hello",
		}}, message["content"])

		u := g.lastUsage(t)
		assert.Equal(t, modelgateway.OutcomeOK, u.Outcome, u.Error)
		assert.Equal(t, int64(5), u.Tokens.Prompt)
		assert.Equal(t, int64(2), u.Tokens.Completion)
	})

	t.Run("a refusal", func(t *testing.T) {
		t.Parallel()

		refusal := `{"error":{"message":"bad tool schema","type":"invalid_request_error"}}`
		upstream, _ := provider(t, plain(http.StatusBadRequest, refusal))
		g := newChatGateway(t, modelgateway.Config{}, upstream)

		status, _, body := g.respond(t, g.key, `{"model":"gpt","input":"hi"}`)
		assert.Equal(t, http.StatusBadRequest, status)
		assert.JSONEq(t, refusal, body)
		assert.Equal(t, modelgateway.OutcomeUpstreamError, g.lastUsage(t).Outcome)
	})

	t.Run("an answer cut at its limit", func(t *testing.T) {
		t.Parallel()

		upstream, _ := provider(t, stream(hello[0],
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"},`+
				`"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`+"\n\n",
			"data: [DONE]\n\n"))
		g := newChatGateway(t, modelgateway.Config{}, upstream)

		status, _, body := g.respond(t, g.key, `{"model":"gpt","input":"hi"}`)
		require.Equal(t, http.StatusOK, status, body)
		assert.Contains(t, body, "event: response.incomplete")

		// The answer ended, at its limit: it was passed on whole and its usage is counted.
		u := g.lastUsage(t)
		assert.Equal(t, modelgateway.OutcomeOK, u.Outcome, u.Error)
		assert.Empty(t, u.Error)
		assert.Equal(t, int64(5), u.Tokens.Prompt)
		assert.Equal(t, int64(2), u.Tokens.Completion)
	})

	t.Run("an error inside the stream", func(t *testing.T) {
		t.Parallel()

		upstream, _ := provider(t, stream(hello[0],
			`data: {"error":{"message":"upstream overloaded","code":"server_error"}}`+"\n\n"))
		g := newChatGateway(t, modelgateway.Config{}, upstream)

		status, _, body := g.respond(t, g.key, `{"model":"gpt","input":"hi"}`)
		require.Equal(t, http.StatusOK, status, body)
		assert.Contains(t, body, "event: response.failed")
		assert.NotContains(t, body, "response.completed")

		u := g.lastUsage(t)
		assert.Equal(t, modelgateway.OutcomeTruncated, u.Outcome)
		assert.Equal(t, "server_error: upstream overloaded", u.Error)
	})
}
