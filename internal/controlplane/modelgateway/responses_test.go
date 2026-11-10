package modelgateway_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// respond sends a Responses request, as the Codex CLI does, and returns the
// status, the headers and the body.
func (g chatGateway) respond(t *testing.T, key, body string, headers ...string) (int, http.Header, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Client-Header", "must not reach the backend")

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)

	return rec.Code, rec.Header(), rec.Body.String()
}

// What a Codex CLI sends, with what the backend refuses of it: a stored
// answer's ids, sampling and limits, metadata.
const codexAsks = `{"model":"sol","instructions":"You are Codex.","input":[` +
	`{"type":"message","role":"user","id":"msg_abc","content":[{"type":"input_text","text":"hi"}]},` +
	`{"type":"item_reference","id":"rs_1"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"ok"}],` +
	`"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],` +
	`"stream":true,"store":true,"temperature":1,"previous_response_id":"resp_9","max_output_tokens":100,` +
	`"metadata":{"a":"b"},"service_tier":"fast","reasoning":{"effort":"high","summary":"auto"},` +
	`"client_metadata":{"x":"y"}}`

// Scenario: a Codex CLI worker calls the gateway in its own format. The
// request goes to the Codex backend under the account logged in on the
// control plane, as the Codex CLI's own, in the client's session; what the
// backend refuses is taken out and the model gets its lite shape. The
// backend's event stream comes back as it was sent, and the usage is recorded.
func TestACodexClientIsPassedThrough(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})

	status, headers, body := g.respond(t, g.key, codexAsks, "session-id", "sess-1")
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))
	assert.Equal(t, strings.Join(helloEvents, ""), body, "the stream is the backend's, event names and all")

	sentHeaders, sentBody := got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	assert.Equal(t, "codex_cli_rs", sentHeaders.Get("Originator"))
	assert.Equal(t, "chatgpt-account", sentHeaders.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "sess-1", sentHeaders.Get("Session_id"))
	assert.Equal(t, "true", sentHeaders.Get("X-Openai-Internal-Codex-Responses-Lite"))
	assert.Empty(t, sentHeaders.Get("X-Client-Header"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(sentBody), &sent))

	assert.Equal(t, "gpt-6.1-sol", sent["model"])
	assert.Equal(t, true, sent["stream"])
	assert.Equal(t, false, sent["store"])
	assert.Equal(t, "priority", sent["service_tier"])
	assert.Equal(t, "sess-1", sent["prompt_cache_key"])
	assert.Equal(t, map[string]any{"x": "y"}, sent["client_metadata"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, sent["include"])
	assert.Equal(t, map[string]any{"effort": "high", "context": "all_turns"}, sent["reasoning"])
	assert.Empty(t, sent["instructions"])
	assert.Nil(t, sent["tools"])

	for _, gone := range []string{"temperature", "previous_response_id", "max_output_tokens", "metadata"} {
		assert.NotContains(t, sent, gone)
	}

	input, _ := sent["input"].([]any)
	require.Len(t, input, 4, "the tools, the instructions, the message and the tool's output; no reference")

	tools, _ := input[0].(map[string]any)
	assert.Equal(t, "additional_tools", tools["type"])
	assert.Len(t, tools["tools"], 1)

	instructions, _ := input[1].(map[string]any)
	assert.Equal(t, "developer", instructions["role"])

	message, _ := input[2].(map[string]any)
	assert.Equal(t, "user", message["role"])
	assert.NotContains(t, message, "id", "the backend stored no item to name")

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeOK, usage.Outcome)
	assert.Equal(t, "codex-subscription", usage.Provider)
	assert.Equal(t, modelgateway.Tokens{Prompt: 30, Completion: 7, Cached: 20, Reasoning: 4}, usage.Tokens)

	// Without a session of its own, a client keeps one per gateway key.
	g.respond(t, g.key, `{"model":"sol","input":"hi"}`)

	sentHeaders, sentBody = got.last(t)
	assert.Len(t, sentHeaders.Get("Session_id"), 36)
	assert.Contains(t, sentBody, `"text":"hi"`, "a plain string is sent as a user's message")
}

// Scenario: the account's usage limit is reached while a Codex CLI works.
// With a second account logged in the same request is served by it; with one
// account the client gets the backend's own refusal, its status and body,
// and the time to wait. A token the backend refuses is refreshed once.
func TestAPassedThroughRequestMovesToTheNextAccount(t *testing.T) {
	t.Parallel()

	resets := time.Now().Add(time.Hour).Unix()
	limit := `{"error":{"type":"usage_limit_reached","message":"limit","resets_at":` + jsonInt(resets) + `}}`

	backend, got := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer access-0":
			plain(http.StatusTooManyRequests, limit)(w, r)
		case "Bearer acc_2-access-0":
			plain(http.StatusUnauthorized, `{"error":{"code":"token_expired"}}`)(w, r)
		default:
			stream(helloEvents...)(w, r)
		}
	})

	two := &accounts{ids: []string{"acc_1", "acc_2"}}
	g := newCodexGateway(t, backend, two)

	status, _, body := g.respond(t, g.key, codexAsks)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 3, got.calls(), "the limited account, the refused token, the new token")
	assert.Equal(t, 1, two.refreshes)
	assert.WithinDuration(t, time.Unix(resets, 0), two.cooledUntil(), time.Second)

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer acc_2-access-1", sentHeaders.Get("Authorization"))

	one := &accounts{}
	alone := newCodexGateway(t, backend, one)

	status, headers, body := alone.respond(t, alone.key, codexAsks)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.JSONEq(t, limit, body)
	assert.NotEmpty(t, headers.Get("Retry-After"))
	assert.Equal(t, modelgateway.OutcomeUpstreamError, alone.lastUsage(t).Outcome)
}

// Scenario: what the route refuses itself. It answers as a stream only; a
// model served by another kind of provider is not passed on here; a key that
// may not use the model, and no key, are refused as on the chat route.
func TestWhatTheResponsesRouteRefuses(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})

	status, _, body := g.respond(t, g.key, `{"model":"sol","input":"hi","stream":false}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "stream_required")

	status, _, body = g.respond(t, g.key, `{"model":"other","input":"hi"}`)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body, "model_not_allowed")

	status, _, _ = g.respond(t, "", `{"model":"sol","input":"hi"}`)
	assert.Equal(t, http.StatusUnauthorized, status)

	status, _, body = g.respond(t, g.key, `not json`)
	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Zero(t, got.calls())

	router, reached := provider(t, plain(http.StatusOK, answer))
	chat := newChatGateway(t, modelgateway.Config{}, router)

	status, _, body = chat.respond(t, chat.key, `{"model":"gpt","input":"hi"}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "model_not_served_here")
	assert.Zero(t, reached.calls())
}

func jsonInt(n int64) string {
	encoded, _ := json.Marshal(n) //nolint:errchkjson // A number always encodes.

	return string(encoded)
}
