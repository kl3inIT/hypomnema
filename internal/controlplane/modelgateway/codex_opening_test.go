package modelgateway_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// A Codex refusal inside a 200 stream before any output (issue #1135), shaped
// like the capture in CLIProxyAPI #6856 (2026-10-21): response.created,
// response.in_progress, a response.metadata frame with the prompt's moderation
// flags, then server_is_overloaded, whose headers carry the Retry-After of
// CLIProxyAPI #7600's capture and the x-retry-metadata of #6856's.
var overloadedOpening = []string{
	event(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response",` +
		`"status":"in_progress","instructions":"You are Codex.","tools":[],"output":[]}}`),
	event(`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","object":"response",` +
		`"status":"in_progress","output":[]}}`),
	event(`{"type":"response.metadata","response_id":"resp_1","sequence_number":2,` +
		`"metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{"omnimod":{"protection_type":"omnimod"}}}}}`),
	event(`{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later.",` +
		`"type":"service_unavailable_error","param":null,"code":"server_is_overloaded",` +
		`"headers":{"retry-after":"326","x-retry-metadata":"NO_MORE_RETRY"}},"sequence_number":3}`),
}

const overloadedText = "server_is_overloaded: Our servers are currently overloaded. Please try again later."

// overloadedFor answers the accounts whose token is refused with the
// overloaded opening, and the others with a whole answer.
func overloadedFor(refused ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, token := range refused {
			if r.Header.Get("Authorization") == "Bearer "+token {
				stream(overloadedOpening...)(w, r)

				return
			}
		}

		stream(helloEvents...)(w, r)
	}
}

// Scenario: a Codex CLI's request reaches an account the backend is overloaded
// on: the backend answers 200, then refuses before any output. The gateway
// sends the request to the next account, which answers; the client sees that
// answer as one clean stream, and the first account rests for the Retry-After
// the refusal named.
func TestAnOverloadBeforeOutputMovesTheResponsesRequestToTheNextAccount(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, overloadedFor("access-0"))
	subs := &accounts{ids: []string{"acc_1", "acc_2"}}
	g := newCodexGateway(t, backend, subs)

	status, _, body := g.respond(t, g.key, `{"model":"sol","input":"hi"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, strings.Join(helloEvents, ""), body, "nothing of the refused stream reaches the client")
	assert.Equal(t, 2, got.calls())

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer acc_2-access-0", sentHeaders.Get("Authorization"))
	assert.Equal(t, []time.Duration{326 * time.Second}, subs.waited("acc_1"))
	assert.Empty(t, subs.waited("acc_2"))

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeOK, usage.Outcome)
	assert.Equal(t, "acc_2", usage.AccountID)
	assert.Equal(t, int64(7), usage.Tokens.Completion)
}

// Scenario: the same on the chat route, which already held a stream until its
// first chunk: the first account now rests for the refusal's Retry-After.
func TestAnOverloadBeforeOutputMovesTheChatToTheNextAccount(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, overloadedFor("access-0"))
	subs := &accounts{ids: []string{"acc_1", "acc_2"}}
	g := newCodexGateway(t, backend, subs)

	status, _, body := g.call(t, g.key, askSol+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.NotContains(t, body, "overloaded")
	assert.Equal(t, 2, got.calls())
	assert.Equal(t, []time.Duration{326 * time.Second}, subs.waited("acc_1"))
	assert.Equal(t, modelgateway.OutcomeOK, g.lastUsage(t).Outcome)
}

// Scenario: every account is overloaded. The client gets one error, the
// refusal's, with its Retry-After; the usage row says what the backend said,
// with the prompt's moderation flags, and counts the input only, no output.
func TestAnOverloadOnEveryAccountIsOneErrorAndARowWithItsFlags(t *testing.T) {
	t.Parallel()

	for _, route := range []string{"responses", "chat"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()

			backend, got := codexBackend(t, overloadedFor("access-0", "acc_2-access-0"))
			subs := &accounts{ids: []string{"acc_1", "acc_2"}}
			g := newCodexGateway(t, backend, subs)

			var (
				status  int
				headers http.Header
				body    string
			)

			if route == "responses" {
				status, headers, body = g.respond(t, g.key, `{"model":"sol","input":"hi"}`)
			} else {
				status, headers, body = g.call(t, g.key, askSol+`,"stream":true}`)
			}

			assert.Equal(t, http.StatusServiceUnavailable, status, body)
			assert.Contains(t, body, "Our servers are currently overloaded")
			assert.NotContains(t, body, "response.created", "no frame of a refused stream reaches the client")
			assert.NotEmpty(t, headers.Get("Retry-After"))
			assert.Equal(t, 2, got.calls())
			assert.Equal(t, []time.Duration{326 * time.Second}, subs.waited("acc_1"))
			assert.Equal(t, []time.Duration{326 * time.Second}, subs.waited("acc_2"))

			usage := g.lastUsage(t)
			assert.Equal(t, modelgateway.OutcomeUpstreamError, usage.Outcome)
			assert.Equal(t, overloadedText+" (moderation: prompt.omnimod.protection_type=omnimod)", usage.Error)
			assert.Zero(t, usage.Tokens.Completion, "a refusal's frames are no output")
			assert.Positive(t, usage.Tokens.Prompt)
			assert.True(t, usage.Tokens.Estimated)
		})
	}
}

// Scenario: the backend refuses after output has reached the client. Nothing
// is sent again, as no reference retries once output started (CLIProxyAPI
// #7481); the row records the cut as before.
func TestAnOverloadAfterOutputIsNotRetried(t *testing.T) {
	t.Parallel()

	frames := []string{
		overloadedOpening[0],
		event(`{"type":"response.output_text.delta","item_id":"msg_1","delta":"hel"}`),
		overloadedOpening[3],
	}

	backend, got := codexBackend(t, stream(frames...))
	g := newCodexGateway(t, backend, &accounts{ids: []string{"acc_1", "acc_2"}})

	status, _, body := g.respond(t, g.key, `{"model":"sol","input":"hi"}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, strings.Join(frames, ""), body)
	assert.Equal(t, 1, got.calls())

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeTruncated, usage.Outcome)
	assert.Equal(t, overloadedText, usage.Error)
	assert.Positive(t, usage.Tokens.Completion)
}

// Scenario: the backend refuses before any output for a reason of the request,
// not of the account. The refusal reaches the client in its stream as before,
// and the row counts no output: the frames before it echo the request.
func TestARequestsOwnFailureBeforeOutputReachesTheClientWithNoOutputCounted(t *testing.T) {
	t.Parallel()

	frames := []string{
		overloadedOpening[0], overloadedOpening[1],
		event(`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded",` +
			`"message":"input too long"}}`),
	}

	backend, got := codexBackend(t, stream(frames...))
	g := newCodexGateway(t, backend, &accounts{ids: []string{"acc_1", "acc_2"}})

	status, _, body := g.respond(t, g.key, `{"model":"sol","input":"hi"}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, strings.Join(frames, ""), body)
	assert.Equal(t, 1, got.calls())

	usage := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeTruncated, usage.Outcome)
	assert.Equal(t, "context_length_exceeded: input too long", usage.Error)
	assert.Zero(t, usage.Tokens.Completion)
}

// Scenario: the backend sends more opening frames than the gateway holds. The
// stream reaches the client whole, as it came, once the bound is reached.
func TestALongOpeningIsReleasedWhole(t *testing.T) {
	t.Parallel()

	frames := []string{overloadedOpening[0]}
	for range 20 {
		frames = append(frames, event(`{"type":"keepalive"}`))
	}

	frames = append(frames, helloEvents[1:]...)

	backend, got := codexBackend(t, stream(frames...))
	g := newCodexGateway(t, backend, &accounts{})

	status, _, body := g.respond(t, g.key, `{"model":"sol","input":"hi"}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, strings.Join(frames, ""), body)
	assert.Equal(t, 1, got.calls())
	assert.Equal(t, modelgateway.OutcomeOK, g.lastUsage(t).Outcome)
}
