package modelgateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
)

// anthropicBackend stands in for api.anthropic.com: it records the path and
// what it gets, and answers with handle.
func anthropicBackend(t *testing.T, handle http.HandlerFunc) (*httptest.Server, *seen, *[]string) {
	t.Helper()

	got, paths := &seen{}, &[]string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		got.add(r)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, got, paths
}

// send posts a Messages request to the gateway with the given headers.
func (g chatGateway) send(t *testing.T, path, body string, headers ...string) (int, http.Header, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)

	return rec.Code, rec.Header(), rec.Body.String()
}

// claudeCodeHeaders are what Claude Code sends with ANTHROPIC_API_KEY set to a
// gateway key.
func claudeCodeHeaders(key string) []string {
	return []string{
		"X-Api-Key", key,
		"User-Agent", "claude-cli/2.1.300 (external, cli)",
		"X-App", "cli",
		"Anthropic-Version", "2023-06-01",
		"Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14",
		"X-Stainless-Lang", "js",
		"X-Claude-Code-Session-Id", "sess-1",
	}
}

const claudeCodeAsk = `{"model":"sol","max_tokens":32000,"stream":true,` +
	`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],` +
	`"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}],` +
	`"metadata":{"user_id":"user_abc_account_def_session_sess-1"}}`

// Scenario: Claude Code, pointed at the gateway with a gateway key in
// x-api-key, streams an answer from the owner's Claude subscription. Its body
// reaches Anthropic as it sent it but for the model, with its own headers and
// the account's token; the events come back as Anthropic sent them, and the
// usage is recorded.
func TestClaudeCodeIsServedFromTheClaudeSubscription(t *testing.T) {
	t.Parallel()

	backend, got, paths := anthropicBackend(t, stream(claudeHello...))
	g := newClaudeGateway(t, backend, &accounts{})

	status, headers, body := g.send(t, "/v1/messages", claudeCodeAsk, claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))
	assert.Equal(t, strings.Join(claudeHello, ""), body, "the events pass as they came")

	assert.Equal(t, []string{"/v1/messages"}, *paths)

	sentHeaders, sent := got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	assert.Empty(t, sentHeaders.Get("X-Api-Key"), "the gateway key never reaches Anthropic")
	assert.Equal(t, "claude-cli/2.1.300 (external, cli)", sentHeaders.Get("User-Agent"))
	assert.Equal(t, "js", sentHeaders.Get("X-Stainless-Lang"))
	assert.Equal(t, "sess-1", sentHeaders.Get("X-Claude-Code-Session-Id"))
	assert.Equal(t, "claude-code-20250219,interleaved-thinking-2025-05-14,oauth-2025-04-20",
		sentHeaders.Get("Anthropic-Beta"))

	var want, request map[string]any
	require.NoError(t, json.Unmarshal([]byte(claudeCodeAsk), &want))
	require.NoError(t, json.Unmarshal([]byte(sent), &request))

	want["model"] = "claude-sonnet-5-5"
	assert.Equal(t, want, request, "only the model changed")

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeOK, usage.Outcome)
	assert.True(t, usage.Stream)
	assert.Equal(t, modelgateway.Tokens{Prompt: 16, Completion: 7, Cached: 4}, usage.Tokens)
}

// Scenario: another tool that speaks Anthropic's format, asking for a whole
// answer with Bearer. Anthropic sees Claude Code: the billing block and the
// identity on top, the tool's own instructions in the first user turn, a user
// id, Claude Code's headers with the beta the tool asked for.
func TestAnotherAnthropicClientIsPresentedAsClaudeCode(t *testing.T) {
	t.Parallel()

	answer := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"hello"}],` +
		`"usage":{"input_tokens":10,"output_tokens":3}}`

	backend, got, _ := anthropicBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	status, _, body := g.send(t, "/v1/messages",
		`{"model":"sol","max_tokens":100,"system":"Be brief.","messages":[{"role":"user","content":"hi"}]}`,
		"Authorization", "Bearer "+g.key, "Anthropic-Beta", "files-api-2025-04-14", "User-Agent", "my-tool/1")
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, answer, body)

	sentHeaders, sent := got.last(t)
	assert.Regexp(t, `^claude-cli/\d`, sentHeaders.Get("User-Agent"))
	assert.Equal(t, "cli", sentHeaders.Get("X-App"))
	assert.Contains(t, sentHeaders.Get("Anthropic-Beta"), "oauth-2025-04-20")
	assert.Contains(t, sentHeaders.Get("Anthropic-Beta"), "files-api-2025-04-14", "the tool's beta is kept")

	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent), &request))
	assert.Equal(t, "claude-sonnet-5-5", request["model"])
	assert.Contains(t, sent, "x-anthropic-billing-header:")
	assert.NotContains(t, request["system"], "Be brief.", "the tool's system is no longer on top")
	assert.Contains(t, sent, "Be brief.", "it moved into the first user turn")

	meta, ok := request["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, meta["user_id"], "device_id")

	usage := g.lastUsage(t)
	assert.False(t, usage.Stream)
	assert.Equal(t, modelgateway.Tokens{Prompt: 10, Completion: 3}, usage.Tokens)
}

// claudeWrites is an answer of Anthropic's that wrote 50 tokens to the cache,
// 30 of them for an hour (usage.cache_creation), as Claude Code's one-hour
// breakpoints write them. Its message_delta repeats the counts without
// cache_creation, as Anthropic's does.
var claudeWrites = []string{
	event(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":4,` +
		`"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":20,` +
		`"ephemeral_1h_input_tokens":30}}}}`),
	event(`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
	event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
	event(`{"type":"content_block_stop","index":0}`),
	event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":12,` +
		`"cache_read_input_tokens":4,"cache_creation_input_tokens":50,"output_tokens":7}}`),
	event(`{"type":"message_stop"}`),
}

// Scenario: Claude Code's answer wrote to the cache, part of it for an hour
// (issue #448). The Messages route records the one-hour part, streamed or
// whole, and prices it at its own rate.
func TestMessagesRecordTheCacheWrittenForAnHour(t *testing.T) {
	t.Parallel()

	whole := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"hello"}],` +
		`"usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":4,` +
		`"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":20,` +
		`"ephemeral_1h_input_tokens":30}}}`

	for name, answer := range map[string]http.HandlerFunc{
		"streamed": stream(claudeWrites...),
		"whole": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, whole)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend, _, _ := anthropicBackend(t, answer)
			g := newClaudeGateway(t, backend, &accounts{})

			ask := claudeCodeAsk
			if name == "whole" {
				ask = strings.Replace(ask, `"stream":true`, `"stream":false`, 1)
			}

			status, _, body := g.send(t, "/v1/messages", ask, claudeCodeHeaders(g.key)...)
			require.Equal(t, http.StatusOK, status, body)

			usage := g.lastUsage(t)
			want := modelgateway.Tokens{Prompt: 66, Completion: 7, Cached: 4, CacheWrite: 50, CacheWrite1h: 30}
			assert.Equal(t, want, usage.Tokens)

			price, ok := prices.Lookup("claude_subscription", "claude-sonnet-5-5")
			require.True(t, ok)
			require.NotNil(t, usage.CostMicros)
			assert.Equal(t, prices.Cost(price, prices.Tokens{
				Input: 66, CacheRead: 4, CacheWrite: 50, CacheWrite1h: 30, Output: 7,
			}), *usage.CostMicros, "the hour's part is priced at its rate")
		})
	}
}

// Scenario: Claude Code counts the tokens of a prompt. Anthropic is asked at
// count_tokens, with the body as sent but for the model; the count comes back
// and no usage is recorded, since nothing was generated.
func TestClaudeCodeCountsTokensAtAnthropic(t *testing.T) {
	t.Parallel()

	backend, got, paths := anthropicBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	ask := `{"model":"sol","messages":[{"role":"user","content":"hi"}]}`
	status, _, body := g.send(t, "/v1/messages/count_tokens", ask, claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `{"input_tokens":42}`, body)
	assert.Equal(t, []string{"/v1/messages/count_tokens"}, *paths)

	_, sent := got.last(t)
	assert.JSONEq(t, `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}`, sent)

	rows, err := g.svc.ListUsage(t.Context(), 1)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// Scenario: what goes wrong is told in Anthropic's shape, which Claude Code
// shows: a key the gateway does not know, a model it does not serve, and
// Anthropic's own refusal passed on with its status and body.
func TestMessagesErrorsAreInAnthropicsShape(t *testing.T) {
	t.Parallel()

	refusal := `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`
	backend, _, _ := anthropicBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, refusal)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	ask := `{"model":"sol","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	status, _, body := g.send(t, "/v1/messages", ask, "X-Api-Key", "hyp_gateway_v1_unknown")
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.JSONEq(t, `{"type":"error","error":{"type":"authentication_error",`+
		`"message":"unknown or revoked gateway key"}}`, body)

	status, _, body = g.send(t, "/v1/messages", strings.Replace(ask, `"sol"`, `"nope"`, 1), "X-Api-Key", g.key)
	assert.Equal(t, http.StatusForbidden, status, "the key is limited to sol")
	assert.Contains(t, body, `"type":"error"`)

	status, _, body = g.send(t, "/v1/messages", ask, "X-Api-Key", g.key)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.JSONEq(t, refusal, body)
	assert.Equal(t, modelgateway.OutcomeUpstreamError, g.lastUsage(t).Outcome)
}

// Scenario: a tool that marks its long system prompt for Anthropic's cache.
// The system moves into the first user turn under the disguise, and keeps the
// tool's breakpoint there, so the prompt is cached and read back next turn.
func TestADisguisedClientKeepsItsCacheBreakpoint(t *testing.T) {
	t.Parallel()

	backend, got, _ := anthropicBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","content":[],"usage":{"input_tokens":1}}`)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	status, _, body := g.send(t, "/v1/messages", `{"model":"sol","max_tokens":10,`+
		`"system":[{"type":"text","text":"Long rules.","cache_control":{"type":"ephemeral"}}],`+
		`"messages":[{"role":"user","content":"hi"}]}`, "X-Api-Key", g.key)
	require.Equal(t, http.StatusOK, status, body)

	_, sent := got.last(t)

	var request struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(sent), &request))
	require.Len(t, request.Messages, 1)

	moved := request.Messages[0].Content[0]
	assert.Contains(t, moved["text"], "Long rules.")
	// Kept an hour, as the gateway's own are on a disguised request (issue #524).
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "1h"}, moved["cache_control"])
}

// claudeSearches is an answer in which the model searched the web three times
// with Anthropic's own web_search tool: message_delta counts them under
// usage.server_tool_use (issue #723).
var claudeSearches = []string{
	event(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":4}}}`),
	event(`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
	event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"found it"}}`),
	event(`{"type":"content_block_stop","index":0}`),
	event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,` +
		`"server_tool_use":{"web_search_requests":3}}}`),
	event(`{"type":"message_stop"}`),
}

// Scenario (issue #723, decision 3.1): Claude Code's model searched the web
// itself. Its row counts the three searches beside its tokens, streamed or
// whole, and its cost is its tokens' and its searches' at LiteLLM's price
// of a search, as Claude Code itself prices them.
func TestMessagesCountTheModelsOwnWebSearches(t *testing.T) {
	t.Parallel()

	whole := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"found it"}],` +
		`"usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":4,` +
		`"server_tool_use":{"web_search_requests":3}}}`

	for name, answer := range map[string]http.HandlerFunc{
		"streamed": stream(claudeSearches...),
		"whole": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, whole)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend, _, _ := anthropicBackend(t, answer)
			g := newClaudeGateway(t, backend, &accounts{})

			ask := claudeCodeAsk
			if name == "whole" {
				ask = strings.Replace(ask, `"stream":true`, `"stream":false`, 1)
			}

			status, _, body := g.send(t, "/v1/messages", ask, claudeCodeHeaders(g.key)...)
			require.Equal(t, http.StatusOK, status, body)

			usage := g.lastUsage(t)
			assert.Equal(t, int64(3), usage.WebSearches)

			tokens, ok := prices.Lookup("claude_subscription", "claude-sonnet-5-5")
			require.True(t, ok)
			search, ok := prices.LookupWebSearch("claude_subscription", "claude-sonnet-5-5")
			require.True(t, ok)
			assert.Equal(t, int64(10_000_000), search.Nanos, "LiteLLM's $0.01 a search")
			require.NotNil(t, usage.CostMicros)
			assert.Equal(t, prices.Cost(tokens, prices.Tokens{Input: 16, CacheRead: 4, Output: 7})+30_000,
				*usage.CostMicros)
		})
	}
}

// Scenario: a Bot's chat holds a photo Anthropic cannot process, from a tool
// result of an earlier turn, and the client sends it again on every turn
// (issue #796). The gateway asks again without the image, with a line in its
// place, so this turn and the next are answered instead of failing.
func TestAnImageTheProviderCannotProcessIsLeftOutAndAskedAgain(t *testing.T) {
	t.Parallel()

	answer := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"done"}],` +
		`"usage":{"input_tokens":3,"output_tokens":1}}`
	var got *seen

	backend, got, _ := anthropicBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		got.mu.Lock()
		body := got.bodies[len(got.bodies)-1]
		got.mu.Unlock()

		if strings.Contains(body, `"type":"image"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error",`+
				`"message":"messages.1.content.0.content.1: Could not process image"}}`)

			return
		}

		_, _ = io.WriteString(w, answer)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	turn := func(ask string) string {
		return `{"model":"sol","max_tokens":100,"stream":false,"messages":[` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"zalo_media","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
			`{"type":"text","text":"photo from Laura"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"AAAA"}}]}]},` +
			`{"role":"user","content":"` + ask + `"}]}`
	}

	for n, ask := range []string{"what is in it?", "do not open any image"} {
		status, _, body := g.send(t, "/v1/messages", turn(ask), claudeCodeHeaders(g.key)...)
		require.Equal(t, http.StatusOK, status, "turn %d: %s", n+1, body)
		assert.JSONEq(t, answer, body)

		_, sent := got.last(t)
		assert.NotContains(t, sent, `"type":"image"`)
		assert.Contains(t, sent, `"text":"photo from Laura"`, "the rest of the tool result stays")
		assert.Contains(t, sent, "omitted image")
		assert.Equal(t, modelgateway.OutcomeOK, g.lastUsage(t).Outcome)
	}

	assert.Equal(t, 4, got.calls(), "each turn: the refused call, then one without the image")
}

// Scenario: a refusal about something else is passed on as it came, and the
// request is not sent again.
func TestARefusalNotAboutAnImageIsNotAskedAgain(t *testing.T) {
	t.Parallel()

	refusal := `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`
	backend, got, _ := anthropicBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, refusal)
	})
	g := newClaudeGateway(t, backend, &accounts{})

	ask := `{"model":"sol","max_tokens":1,"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`

	status, _, body := g.send(t, "/v1/messages", ask, claudeCodeHeaders(g.key)...)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.JSONEq(t, refusal, body)
	assert.Equal(t, 1, got.calls())
}
