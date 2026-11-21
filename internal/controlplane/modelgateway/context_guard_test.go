package modelgateway_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/contextwindow"
)

// windowed gives the gateway's model `sol` a window of 2,000 tokens.
func (g chatGateway) windowed(t *testing.T) chatGateway {
	t.Helper()

	_, err := g.svc.ApplyModel(t.Context(),
		"name: sol\ncontext_window: 2000\ntargets:\n  - {provider: codex-subscription, model: gpt-6.1-sol}\n")
	require.NoError(t, err)

	return g
}

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

// Scenario: a prompt that cannot fit the model's window (issue #1052). It is
// refused before it is sent, with the window in the error and the code Codex
// reads as its context-window overflow; an inline image counts as 1,200
// tokens, not as its bytes, so a short prompt with a large image is sent.
func TestAPromptOverTheWindowIsRefusedBeforeItIsSent(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{}).windowed(t)

	status, _, body := g.respond(t, g.key, asking(20000))
	require.Equal(t, http.StatusBadRequest, status, body)
	code, message := refusal(t, body)
	assert.Equal(t, "context_length_exceeded", code)
	assert.Contains(t, message, "limit 2000")
	assert.Zero(t, got.calls(), "nothing was sent")

	image := `{"model":"sol","stream":true,"input":[{"type":"message","role":"user","content":[` +
		`{"type":"input_text","text":"what is it?"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 40000) + `"}]}]}`
	status, _, body = g.respond(t, g.key, image)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 1, got.calls())
}

// Scenario: a client that keeps sending prompts over the window, as Codex
// does while it compacts (issue #1052). The first three are refused as
// overflows, for it to shorten; the fourth ends its turn with a code it does
// not retry, whose message names the window, and for the cooldown the key
// sends nothing more to the model, even a prompt that fits.
func TestARunOfOverflowsEndsTheTurn(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{}).windowed(t)

	for range 3 {
		status, _, body := g.respond(t, g.key, asking(20000))
		require.Equal(t, http.StatusBadRequest, status, body)
		code, _ := refusal(t, body)
		assert.Equal(t, "context_length_exceeded", code)
	}

	for _, ask := range []string{asking(20000), asking(10)} {
		status, _, body := g.respond(t, g.key, ask)
		require.Equal(t, http.StatusBadRequest, status, body)
		code, message := refusal(t, body)
		assert.Equal(t, "context_overflow", code)
		assert.True(t, contextwindow.Exceeded(message), "the node tells why the turn ended: %s", message)
	}

	assert.Zero(t, got.calls())
}

// Scenario: the provider itself says the prompt did not fit (issue #1052),
// as the Codex backend answered on 2026-11-19. Four such answers in a row
// are passed on; the fifth request is not sent. An answer that ends well
// between them starts the count again.
func TestTheProvidersOverflowAnswersCount(t *testing.T) {
	t.Parallel()

	const overflowed = `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded",` +
		`"message":"Your input exceeds the context window of this model."}}}`

	var answerWell atomic.Bool
	backend, got := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if answerWell.Load() {
			stream(helloEvents...)(w, r)

			return
		}

		stream(event(`{"type":"response.created","response":{"id":"resp_1"}}`), event(overflowed))(w, r)
	})
	g := newCodexGateway(t, backend, &accounts{})

	for range 3 {
		status, _, _ := g.respond(t, g.key, asking(10))
		require.Equal(t, http.StatusOK, status)
	}

	answerWell.Store(true)
	status, _, _ := g.respond(t, g.key, asking(10))
	require.Equal(t, http.StatusOK, status)
	answerWell.Store(false)

	for range 4 {
		status, _, _ := g.respond(t, g.key, asking(10))
		require.Equal(t, http.StatusOK, status)
	}

	assert.Equal(t, 8, got.calls())

	status, _, body := g.respond(t, g.key, asking(10))
	require.Equal(t, http.StatusBadRequest, status, body)
	code, _ := refusal(t, body)
	assert.Equal(t, "context_overflow", code)
	assert.Equal(t, 8, got.calls(), "the fifth was not sent")
}
