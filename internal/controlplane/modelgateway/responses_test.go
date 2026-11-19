package modelgateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
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

func TestResponsesRecordsWhyTheStreamEndedEarly(t *testing.T) {
	t.Parallel()

	const partial = `{"type":"response.output_text.delta","delta":"hello"}`
	for _, tc := range []struct {
		name, failure, reason string
		idle                  bool
	}{
		{"response.failed", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"overloaded"}}}`,
			"server_error: overloaded", false},
		{"error", `{"type":"error","code":"context_length_exceeded","message":"input too long"}`,
			"context_length_exceeded: input too long", false},
		{"idle timeout", "", "no event for 150ms", true},
		{"upstream close", "", "upstream closed before response.completed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			frames := []string{event(partial)}
			if tc.failure != "" {
				frames = append(frames, event(tc.failure))
			}

			backend, _ := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
				stream(frames...)(w, r)
				if tc.idle {
					<-r.Context().Done()
				}
			})
			cfg := modelgateway.Config{Subscriptions: &accounts{}}
			if tc.idle {
				cfg.StreamIdleTimeout = 150 * time.Millisecond
			}
			g := newCodexGatewayWithConfig(t, backend, cfg)
			status, _, body := g.respond(t, g.key, `{"model":"sol","input":"hi"}`)
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(t, strings.Join(frames, ""), body)
			usage := g.lastUsage(t)
			assert.Equal(t, modelgateway.OutcomeTruncated, usage.Outcome)
			assert.Equal(t, tc.reason, usage.Error)
		})
	}
}

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
	assert.NotNil(t, usage.TTFT, "the Responses stream has its time to first token (issue #856)")
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

// Scenario: every Codex account already waits for its limit when a Codex CLI
// worker calls (issue #756). The backend is not called, and the worker gets
// the backend's own usage-limit shape with the reset, which the CLI ends its
// turn on, instead of a 429 it would retry after the whole wait, five times.
// A wait of a minute or less stays a plain 429.
func TestAWorkerWhoseAccountsAllCoolGetsAUsageLimit(t *testing.T) {
	t.Parallel()

	resets := time.Now().Add(time.Hour).Truncate(time.Second)
	limit := `{"error":{"type":"usage_limit_reached","message":"limit","resets_at":` + jsonInt(resets.Unix()) + `}}`

	backend, got := codexBackend(t, plain(http.StatusTooManyRequests, limit))
	g := newCodexGateway(t, backend, &accounts{})

	status, _, _ := g.respond(t, g.key, codexAsks)
	require.Equal(t, http.StatusTooManyRequests, status)

	status, headers, body := g.respond(t, g.key, codexAsks)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Equal(t, 1, got.calls(), "a cooling account is not sent to the backend")
	assert.JSONEq(t, `{"error":{"type":"usage_limit_reached","code":"account_unavailable",`+
		`"message":"no account of this provider can be used for now","resets_at":`+jsonInt(resets.Unix())+`}}`, body)
	assert.NotEmpty(t, headers.Get("Retry-After"))

	// A wait of a minute or less is the client's to sit out: a plain 429 the CLI retries after Retry-After.
	soon := time.Now().Add(30 * time.Second).Unix()
	backendSoon, _ := codexBackend(t, plain(http.StatusTooManyRequests,
		`{"error":{"type":"usage_limit_reached","message":"limit","resets_at":`+jsonInt(soon)+`}}`))
	shortly := newCodexGateway(t, backendSoon, &accounts{})
	shortly.respond(t, shortly.key, codexAsks)

	status, headers, body = shortly.respond(t, shortly.key, codexAsks)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, body, `"type":"rate_limit_error"`)
	assert.Contains(t, body, "account_unavailable")
	assert.NotEmpty(t, headers.Get("Retry-After"))
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

// leavingClient is a client that reads the stream and leaves as it reads a mark: its request ends, and what is
// written after fails. With the mark "response.completed" it is the Codex CLI, which returns from its read as it
// reads that event (codex-rs/codex-api/src/sse/responses.rs).
type leavingClient struct {
	*httptest.ResponseRecorder

	mark  string
	leave context.CancelFunc
	gone  bool
}

func (c *leavingClient) Write(p []byte) (int, error) {
	if c.gone {
		return 0, errors.New("the client closed the connection")
	}

	n, err := c.ResponseRecorder.Write(p)
	if strings.Contains(c.Body.String(), c.mark) && strings.HasSuffix(c.Body.String(), "\n\n") {
		c.gone = true
		c.leave()
	}

	return n, err
}

// Scenario (issue #635): a Codex CLI reads the answer to its end and closes the connection at once. The request was
// answered: it is recorded ok, with its tokens. One that leaves in the middle of the answer is recorded as a client
// gone.
func TestAClientThatLeavesAfterTheEndWasAnswered(t *testing.T) {
	t.Parallel()

	backend, _ := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})

	for mark, want := range map[string]modelgateway.Outcome{
		`"response.completed"`: modelgateway.OutcomeOK,
		`"delta":"hel"`:        modelgateway.OutcomeClientGone,
	} {
		ctx, leave := context.WithCancel(t.Context())
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(codexAsks))
		req.Header.Set("Authorization", "Bearer "+g.key)

		g.mux.ServeHTTP(&leavingClient{ResponseRecorder: httptest.NewRecorder(), mark: mark, leave: leave}, req)
		leave()

		usage := g.lastUsage(t)
		assert.Equal(t, want, usage.Outcome, mark)

		if want == modelgateway.OutcomeOK {
			assert.Equal(t, modelgateway.Tokens{Prompt: 30, Completion: 7, Cached: 20, Reasoning: 4}, usage.Tokens)
		}
	}
}

// Scenario (issue #723, decision 3.1): a Codex worker's model searched the
// web twice with the hosted web_search tool. Each search is a web_search_call
// item, told when it is added and when it is done; the row counts two
// searches beside the tokens, each priced at LiteLLM's price of a search.
func TestResponsesCountTheModelsOwnWebSearches(t *testing.T) {
	t.Parallel()

	searched := []string{
		event(`{"type":"response.created","response":{"id":"resp_1"}}`),
		event(`{"type":"response.output_item.added","item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}`),
		event(`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed",` +
			`"action":{"type":"search","query":"go 1.27"}}}`),
		event(`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_2","status":"completed"}}`),
		event(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1"}}`),
		helloEvents[3],
	}

	backend, _ := codexBackend(t, stream(searched...))
	g := newCodexGateway(t, backend, &accounts{})

	status, _, body := g.respond(t, g.key, codexAsks, "session-id", "sess-1")
	require.Equal(t, http.StatusOK, status, body)

	usage := g.lastUsage(t)
	assert.Equal(t, int64(2), usage.WebSearches)
	assert.Equal(t, modelgateway.Tokens{Prompt: 30, Completion: 7, Cached: 20, Reasoning: 4}, usage.Tokens)

	tokens, ok := prices.Lookup("codex_subscription", "gpt-6.1-sol")
	require.True(t, ok)
	require.NotNil(t, usage.CostMicros)
	assert.Equal(t, prices.Cost(tokens, prices.Tokens{Input: 30, CacheRead: 20, Output: 7})+20_000, *usage.CostMicros)
}
