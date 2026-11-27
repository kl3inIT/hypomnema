package modelgateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/contextwindow"
)

// refusal is the error of a gateway's answer.
func refusal(t *testing.T, body string) (code, message string) {
	t.Helper()

	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &e), body)

	return e.Error.Code, e.Error.Message
}

// asking is a Responses request whose text is n bytes long.
func asking(n int) string {
	return `{"model":"sol","stream":true,"input":[{"type":"message","role":"user","content":[` +
		`{"type":"input_text","text":"` + strings.Repeat("x", n) + `"}]}]}`
}

// failing is a Codex backend whose answers fail with the event failure, or end well while well is set.
func failing(t *testing.T, failure string, well *atomic.Bool) (*httptest.Server, *seen) {
	t.Helper()

	return codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if well.Load() {
			stream(helloEvents...)(w, r)

			return
		}

		stream(event(`{"type":"response.created","response":{"id":"resp_1"}}`), event(failure))(w, r)
	})
}

// Scenario: the gateway does not count a prompt's tokens (issue #1052,
// review of PR #1057): bytes over four refused prompts that fit, and LiteLLM
// checks a prompt against the window only when asked to. A prompt larger
// than the model's window by its bytes is sent, and the provider answers.
func TestAPromptIsSentWhateverItsSize(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})
	_, err := g.svc.ApplyModel(t.Context(),
		"name: sol\ncontext_window: 2000\ntargets:\n  - {provider: codex-subscription, model: gpt-6.1-sol}\n")
	require.NoError(t, err)

	status, _, body := g.respond(t, g.key, asking(20000))
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 1, got.calls())
}

// Scenario: the provider says the prompt did not fit (issue #1052), as the
// Codex backend answered on 2026-11-19 while Codex trimmed and resent its
// history. Four such answers in a row are passed on; then for the cooldown
// the key's requests to the model are not sent, and the refusal is one
// Codex does not retry, so its turn ends, with a message that names the
// context window for the node to tell why. An answer that ends well between
// them starts the count again.
func TestARunOfOverflowAnswersEndsTheTurn(t *testing.T) {
	t.Parallel()

	const overflowed = `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded",` +
		`"message":"Your input exceeds the context window of this model."}}}`

	var well atomic.Bool
	backend, got := failing(t, overflowed, &well)
	g := newCodexGateway(t, backend, &accounts{})

	for range 3 {
		status, _, _ := g.respond(t, g.key, asking(10))
		require.Equal(t, http.StatusOK, status)
	}

	well.Store(true)
	status, _, _ := g.respond(t, g.key, asking(10))
	require.Equal(t, http.StatusOK, status)
	well.Store(false)

	for range 4 {
		status, _, _ := g.respond(t, g.key, asking(10))
		require.Equal(t, http.StatusOK, status)
	}

	assert.Equal(t, 8, got.calls())

	status, _, body := g.respond(t, g.key, asking(10))
	require.Equal(t, http.StatusBadRequest, status, body)
	code, message := refusal(t, body)
	assert.Equal(t, "context_overflow", code)
	assert.True(t, contextwindow.Exceeded(message), "the node tells why the turn ended: %s", message)
	assert.Equal(t, 8, got.calls(), "the fifth was not sent")
}

// Scenario: the provider refuses a tool's arguments as too long (review of
// PR #1057). That is no overflow of the context window: however many in a
// row, the next request is sent.
func TestAToolInputTooLongIsNoOverflow(t *testing.T) {
	t.Parallel()

	const refused = `{"type":"response.failed","response":{"error":{"code":"invalid_value",` +
		`"message":"Tool input is too long"}}}`

	var well atomic.Bool
	backend, got := failing(t, refused, &well)
	g := newCodexGateway(t, backend, &accounts{})

	for range 5 {
		status, _, _ := g.respond(t, g.key, asking(10))
		require.Equal(t, http.StatusOK, status)
	}

	assert.Equal(t, 5, got.calls())
}

// Scenario: the reviewer's repro of round 2 of PR #1057 (issue #1052). The
// answers end without a word: cut short after response.created, or the
// client gone before anything came. Whatever the prompt's size, after four
// in a row with none that ended well, the fifth request is not sent, and its
// refusal ends the turn as the provider's overflow answers do.
func TestARunOfAnswersThatDoNotEndEndsTheTurn(t *testing.T) {
	t.Parallel()

	for name, clientGone := range map[string]bool{"silent truncation": false, "empty client_gone": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handle := stream(event(`{"type":"response.created","response":{"id":"resp_1"}}`))
			var arrived chan struct{}
			if clientGone {
				arrived = make(chan struct{})
				handle = func(_ http.ResponseWriter, r *http.Request) {
					select {
					case arrived <- struct{}{}:
						<-r.Context().Done()
					case <-r.Context().Done():
					}
				}
			}
			backend, got := codexBackend(t, handle)
			g := newCodexGateway(t, backend, &accounts{})

			ask := func() (int, string) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()

				if clientGone {
					// Disconnect only after the provider receives the request. The timeout is a deadlock bound.
					go func() {
						select {
						case <-arrived:
							cancel()
						case <-ctx.Done():
						}
					}()
				}

				req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(asking(10)))
				req.Header.Set("Authorization", "Bearer "+g.key)
				rec := httptest.NewRecorder()
				g.mux.ServeHTTP(rec, req)

				return rec.Code, rec.Body.String()
			}

			for attempt := range 4 {
				ask()
				require.Equal(t, attempt+1, got.calls())
			}

			status, body := ask()
			require.Equal(t, http.StatusBadRequest, status, body)
			code, message := refusal(t, body)
			assert.Equal(t, "context_overflow", code)
			assert.True(t, contextwindow.Exceeded(message), "the node tells why the turn ended: %s", message)
			assert.Equal(t, 4, got.calls(), "the fifth was not sent")
		})
	}
}

// Scenario: review round 3 of PR #1057 (issue #1052). The provider ends each
// stream on an error Codex retries on its own, up to five times
// (codex-rs/codex-api/src/sse/responses_error.rs:73-91,
// model-provider-info/src/lib.rs:74): a truncated answer like any other.
// After four in a row the fifth request is not sent.
func TestReviewRound3RetryableTruncation(t *testing.T) {
	t.Parallel()

	const overloaded = `{"type":"response.failed","response":{"error":{"code":"server_error","message":"overloaded"}}}`

	var well atomic.Bool
	backend, got := failing(t, overloaded, &well)
	g := newCodexGateway(t, backend, &accounts{})

	for range 4 {
		status, _, _ := g.respond(t, g.key, asking(10))
		require.Equal(t, http.StatusOK, status)
	}

	status, _, body := g.respond(t, g.key, asking(10))
	require.Equal(t, http.StatusBadRequest, status, body)
	code, _ := refusal(t, body)
	assert.Equal(t, "context_overflow", code)
	assert.Equal(t, 4, got.calls(), "the fifth was not sent")
}
