package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// claudeBackend stands in for api.anthropic.com/v1/messages: it records what
// it gets and answers with handle.
func claudeBackend(t *testing.T, handle http.HandlerFunc) (*httptest.Server, *seen) {
	t.Helper()

	got := &seen{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)

			return
		}

		got.add(r)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, got
}

// newClaudeGateway returns a gateway whose model `sol` is served from the
// built-in Claude provider.
func newClaudeGateway(t *testing.T, backend *httptest.Server, subs modelgateway.Subscriptions) chatGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	svc := modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			p, err := orch.GetProvider(ctx, name)
			if err != nil {
				return modelgateway.ProviderInfo{}, fmt.Errorf("%w: %w", modelgateway.ErrNotFound, err)
			}

			return modelgateway.ProviderInfo{
				Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, APIKeyRef: p.APIKeyRef, Models: p.Models,
			}, nil
		},
		func(_ context.Context, ref string) (string, error) { return "sk-of-" + ref, nil })

	_, err = svc.ApplyModel(t.Context(),
		"name: sol\ntargets:\n  - {provider: claude-subscription, model: claude-sonnet-5-5}\n")
	require.NoError(t, err, "the provider is built in: no one declared it")

	_, key, err := svc.AddKey(t.Context(), "pi", []string{"sol"})
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"),
		modelgateway.Config{Subscriptions: subs, ClaudeBaseURL: backend.URL}).Register(mux)

	return chatGateway{svc: svc, mux: mux, key: key}
}

var claudeHello = []string{
	event(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":4}}}`),
	event(`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
	event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`),
	event(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`),
	event(`{"type":"content_block_stop","index":0}`),
	event(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`),
	event(`{"type":"message_stop"}`),
}

const askClaude = `{"model":"sol","messages":[{"role":"system","content":"Be brief."},` +
	`{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]`

// Scenario: pi streams a chat with the model the gateway serves from the
// owner's Claude subscription. The backend is called at /v1/messages as Claude
// Code calls it — a Bearer subscription token, the oauth beta, and a body
// carrying the billing block and the metadata user id — under the account
// logged in on the control plane; pi gets OpenAI's chunks and the end mark.
func TestAChatIsServedFromTheClaudeSubscription(t *testing.T) {
	t.Parallel()

	backend, got := claudeBackend(t, stream(claudeHello...))
	g := newClaudeGateway(t, backend, &accounts{})

	status, headers, body := g.call(t, g.key, askClaude+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))

	sentHeaders, sent := got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	assert.Empty(t, sentHeaders.Get("X-Api-Key"), "a subscription token never goes in the api-key header")
	assert.Contains(t, sentHeaders.Get("Anthropic-Beta"), "oauth-2025-04-20")
	assert.Contains(t, sentHeaders.Get("Anthropic-Beta"), "extended-cache-ttl-2025-04-11",
		"the one-hour breakpoints go with their beta")
	assert.Regexp(t, `^claude-cli/\d`, sentHeaders.Get("User-Agent"))
	assert.Equal(t, "cli", sentHeaders.Get("X-App"))
	assert.Empty(t, sentHeaders.Get("X-Client-Header"), "a client header never reaches the backend")

	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent), &request))
	assert.Equal(t, "claude-sonnet-5-5", request["model"])
	assert.Equal(t, true, request["stream"])

	system, ok := request["system"].([]any)
	require.True(t, ok, "system is a list")
	billing, ok := system[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, billing["text"], "x-anthropic-billing-header:")
	meta, ok := request["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, meta["user_id"], "device_id")

	// The client's one tool went out among the decoys, renamed.
	tools, ok := request["tools"].([]any)
	require.True(t, ok)
	assert.Greater(t, len(tools), 1, "the decoys were added")

	lines := dataLines(body)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, `"content":"hel"`)
	assert.Contains(t, joined, `"content":"lo"`)
	assert.Equal(t, "[DONE]", lines[len(lines)-1])
	assert.NotContains(t, joined, `"usage"`, "a client that did not ask for the usage frame does not get it")

	// The gateway reads Claude's own counts, the cache's too (issue #389).
	u := g.lastUsage(t)
	assert.False(t, u.Tokens.Estimated)
	assert.EqualValues(t, 16, u.Tokens.Prompt)
	assert.EqualValues(t, 4, u.Tokens.Cached)
	assert.EqualValues(t, 7, u.Tokens.Completion)
}

// Scenario: a client that does not stream gets one completion.
func TestAClaudeClientThatDoesNotStreamGetsOneCompletion(t *testing.T) {
	t.Parallel()

	backend, _ := claudeBackend(t, stream(claudeHello...))
	g := newClaudeGateway(t, backend, &accounts{})

	status, headers, body := g.call(t, g.key, askClaude+`}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))

	var completion map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &completion))
	assert.Equal(t, "chat.completion", completion["object"])
	choices, ok := completion["choices"].([]any)
	require.True(t, ok)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok)
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "hello", message["content"])
	assert.Equal(t, "stop", choice["finish_reason"])
}

// Scenario: a token Anthropic refuses is refreshed once and the request sent
// again under the new token.
func TestAClaudeRefusedTokenIsRefreshed(t *testing.T) {
	t.Parallel()

	var calls int

	backend, got := claudeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error"}}`, http.StatusUnauthorized)

			return
		}

		stream(claudeHello...)(w, r)
	})

	g := newClaudeGateway(t, backend, &accounts{})

	status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 2, got.calls(), "the request was sent again after the refresh")

	first, _ := got.headers[0], got.bodies[0]
	assert.Equal(t, "Bearer access-0", first.Get("Authorization"))
	assert.Equal(t, "Bearer access-1", got.headers[1].Get("Authorization"), "the refreshed token")
}

// Scenario: a usage limit (429) cools the account down.
func TestAClaudeUsageLimitCoolsTheAccount(t *testing.T) {
	t.Parallel()

	backend, _ := claudeBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"type":"error","error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	})

	acc := &accounts{}
	g := newClaudeGateway(t, backend, acc)

	status, headers, _ := g.call(t, g.key, askClaude+`,"stream":true}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.NotEmpty(t, headers.Get("Retry-After"))
	assert.False(t, acc.cooledUntil().IsZero(), "the account was cooled down")
	assert.Equal(t, "usage limit", acc.reason)
}

// Scenario: Anthropic says on every answer how much of the account's 5-hour
// and 7-day windows is used. The gateway passes on what is left under the
// fuller of the two, so the accounts can keep one that is nearly used up for
// last; an answer that says nothing leaves the account as it was.
func TestAClaudeAnswerSaysWhatIsLeftOfTheAccount(t *testing.T) {
	t.Parallel()

	var calls int

	backend, _ := claudeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
			w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.93")
		}

		stream(claudeHello...)(w, r)
	})

	acc := &accounts{}
	g := newClaudeGateway(t, backend, acc)

	_, said := acc.quotaNoted("acc_1")
	require.False(t, said, "nothing is known before the first answer")

	status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)

	left, said := acc.quotaNoted("acc_1")
	require.True(t, said)
	assert.InDelta(t, 0.07, left, 1e-9, "one minus the fuller window")
	assert.Equal(t, "claude 0.42", acc.answerNoted("acc_1"), "the accounts get the answer's headers for the limits")

	status, _, body = g.call(t, g.key, askClaude+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)

	left, _ = acc.quotaNoted("acc_1")
	assert.InDelta(t, 0.07, left, 1e-9, "an answer without the headers changes nothing")
}
