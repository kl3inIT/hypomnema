package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// seen is what a provider received.
type seen struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
}

func (s *seen) add(r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.headers = append(s.headers, r.Header.Clone())
	s.bodies = append(s.bodies, string(body))
}

func (s *seen) last(t *testing.T) (http.Header, string) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	require.NotEmpty(t, s.bodies, "the provider was not called")

	return s.headers[len(s.headers)-1], s.bodies[len(s.bodies)-1]
}

func (s *seen) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.bodies)
}

// provider starts an OpenAI-compatible provider that records what it gets
// and answers with handle.
func provider(t *testing.T, handle http.HandlerFunc) (*httptest.Server, *seen) {
	t.Helper()

	got := &seen{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)

			return
		}

		got.add(r)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, got
}

// stream answers with the given event stream frames, flushing each.
func stream(frames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		for _, f := range frames {
			_, _ = io.WriteString(w, f)

			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

func plain(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

type chatGateway struct {
	svc *modelgateway.Service
	mux *http.ServeMux
	key string
}

// newChatGateway returns a gateway that serves the model `gpt` from the
// given providers in order, as providers p1, p2, …, each offering `up-N`
// with its key in the vault as `vault:pN`, and a key that may use `gpt` only.
func newChatGateway(t *testing.T, cfg modelgateway.Config, providers ...*httptest.Server) chatGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	model := "name: gpt\ntargets:\n"

	for i, p := range providers {
		n := i + 1

		_, err := orch.ApplyProvider(t.Context(), "", fmt.Sprintf(
			"name: p%d\nkind: openai_compatible\nbase_url: %s/v1\napi_key_ref: vault:p%d\nmodels: [up-%d]\n",
			n, p.URL, n, n))
		require.NoError(t, err)

		model += fmt.Sprintf("  - {provider: p%d, model: up-%d}\n", n, n)
	}

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

	_, err = svc.ApplyModel(t.Context(), model)
	require.NoError(t, err)

	_, key, err := svc.AddKey(t.Context(), "pi", []string{"gpt"})
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"), cfg).Register(mux)

	return chatGateway{svc: svc, mux: mux, key: key}
}

// call sends a chat request with the gateway's key and returns the status,
// the headers and the body.
func (g chatGateway) call(t *testing.T, key, body string) (int, http.Header, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Client-Header", "must not reach the provider")

	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)

	return rec.Code, rec.Header(), rec.Body.String()
}

// lastUsage returns the record of the latest request.
func (g chatGateway) lastUsage(t *testing.T) modelgateway.Usage {
	t.Helper()

	rows, err := g.svc.ListUsage(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

const answer = `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":` +
	`{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,` +
	`"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":4},` +
	`"completion_tokens_details":{"reasoning_tokens":2}},"provider_field":{"kept":true}}`

// Scenario: pi asks for a model by its public name. The provider gets the
// request as pi sent it, fields the gateway does not know and a number too
// large for a float included, under the provider's name for the model and
// the provider's key; pi gets the provider's answer as it was; the tokens the
// provider reported are recorded.
func TestAChatRequestIsForwardedAsItCame(t *testing.T) {
	t.Parallel()

	upstream, got := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	status, headers, body := g.call(t, g.key, `{"model":"gpt","messages":[{"role":"user","content":"hi",`+
		`"a_field_inside":1}],"seed":9007199254740993,"reasoning_effort":"high","a_new_parameter":{"b":[1,2]}}`)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded

	sentHeaders, sent := got.last(t)
	assert.JSONEq(t, `{"model":"up-1","messages":[{"role":"user","content":"hi","a_field_inside":1}],`+
		`"seed":9007199254740993,"reasoning_effort":"high","a_new_parameter":{"b":[1,2]}}`, sent)
	assert.Contains(t, sent, "9007199254740993", "a large integer must not pass through a float")
	assert.Equal(t, "Bearer sk-of-vault:p1", sentHeaders.Get("Authorization"))
	assert.Empty(t, sentHeaders.Get("X-Client-Header"))

	u := g.lastUsage(t)
	assert.Equal(t, "pi", u.KeyName)
	assert.Equal(t, "gpt", u.Model)
	assert.Equal(t, "p1", u.Provider)
	assert.Equal(t, "up-1", u.UpstreamModel)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)
	assert.Equal(t, http.StatusOK, u.Status)
	assert.Equal(t, 1, u.Attempts)
	assert.False(t, u.Stream)
	assert.Equal(t, modelgateway.Tokens{Prompt: 12, Completion: 5, Cached: 4, Reasoning: 2}, u.Tokens)
}

const (
	chunkRole   = `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`
	chunkText   = `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}],"x":1}`
	chunkFinish = `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	chunkUsage  = `{"id":"c1","object":"chat.completion.chunk","choices":[],` +
		`"usage":{"prompt_tokens":30,"completion_tokens":7}}`
	frameDone    = "data: [DONE]\n\n"
	frameComment = ": keep-alive\n\n"
)

func frame(payload string) string { return "data: " + payload + "\n\n" }

// Scenario: pi streams and did not ask for usage. The gateway asks the
// provider for it, records it, and keeps the usage-only frame from pi; every
// other frame reaches pi as the provider sent it, and comments do not.
func TestAStreamIsAskedForItsUsageWhichTheClientDoesNotSee(t *testing.T) {
	t.Parallel()

	upstream, got := provider(t, stream(
		frameComment, frame(chunkRole), frame(chunkText), frame(chunkFinish), frame(chunkUsage), frameDone))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	status, headers, body := g.call(t, g.key, `{"model":"gpt","stream":true,"messages":[]}`)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))
	assert.Equal(t, frame(chunkRole)+frame(chunkText)+frame(chunkFinish)+frameDone, body)

	_, sent := got.last(t)
	assert.JSONEq(t, `{"model":"up-1","stream":true,"messages":[],"stream_options":{"include_usage":true}}`, sent)

	u := g.lastUsage(t)
	assert.True(t, u.Stream)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)
	assert.Equal(t, modelgateway.Tokens{Prompt: 30, Completion: 7}, u.Tokens)
}

// Scenario: a client streams and asked for usage, with another stream option
// beside it. It gets the usage frame, and its other option reaches the
// provider.
func TestAClientThatAskedForUsageGetsTheUsageFrame(t *testing.T) {
	t.Parallel()

	upstream, got := provider(t, stream(frame(chunkText), frame(chunkFinish), frame(chunkUsage), frameDone))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	_, _, body := g.call(t, g.key,
		`{"model":"gpt","stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false}}`)

	assert.Equal(t, frame(chunkText)+frame(chunkFinish)+frame(chunkUsage)+frameDone, body)

	_, sent := got.last(t)
	assert.JSONEq(t, `{"model":"up-1","stream":true,`+
		`"stream_options":{"include_usage":true,"include_obfuscation":false}}`, sent)
}

// Scenario: the first provider is overloaded, then unreachable. Each time
// the next target answers instead, under its own name for the model and its
// own key, and the client sees only that answer.
func TestTheNextTargetAnswersWhenTheFirstCannot(t *testing.T) {
	t.Parallel()

	overloaded, firstGot := provider(t, plain(http.StatusServiceUnavailable, `{"error":{"message":"overloaded"}}`))
	healthy, secondGot := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, overloaded, healthy)

	status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded
	assert.Equal(t, 1, firstGot.calls())

	sentHeaders, sent := secondGot.last(t)
	assert.JSONEq(t, `{"model":"up-2","messages":[]}`, sent)
	assert.Equal(t, "Bearer sk-of-vault:p2", sentHeaders.Get("Authorization"))

	u := g.lastUsage(t)
	assert.Equal(t, 2, u.Attempts)
	assert.Equal(t, "p2", u.Provider)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)

	overloaded.Close()

	status, _, body = g.call(t, g.key, `{"model":"gpt","messages":[]}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded
	assert.Equal(t, 2, g.lastUsage(t).Attempts)
}

// Scenario: the provider refuses the request, or the last target is
// limiting. The client gets the provider's own status and body, with its
// Retry-After, and the record says the provider answered with an error. A
// refusal is not tried on the next target: another provider would refuse the
// same request.
func TestAProvidersErrorReachesTheClientAsItIs(t *testing.T) {
	t.Parallel()

	refusal := `{"error":{"message":"messages is too long","type":"invalid_request_error"}}`
	refusing, _ := provider(t, plain(http.StatusBadRequest, refusal))
	never, neverGot := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, refusing, never)

	status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, refusal, body)
	assert.Zero(t, neverGot.calls())

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeUpstreamError, u.Outcome)
	assert.Equal(t, http.StatusBadRequest, u.Status)
	assert.Equal(t, modelgateway.Tokens{}, u.Tokens)

	limiting, _ := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
	})
	only := newChatGateway(t, modelgateway.Config{}, limiting)

	status, headers, body := only.call(t, only.key, `{"model":"gpt","messages":[]}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Equal(t, "17", headers.Get("Retry-After"))
	assert.JSONEq(t, `{"error":{"message":"slow down"}}`, body)
}

// Scenario: no provider can be reached. The client gets a 502 in OpenAI's
// error shape, which a client retries, and the record says so.
func TestNoProviderReachableIsABadGateway(t *testing.T) {
	t.Parallel()

	gone, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, gone)
	gone.Close()

	status, _, body := g.call(t, g.key, `{"model":"gpt","messages":[]}`)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.JSONEq(t, `{"error":{"message":"no provider of model \"gpt\" could be reached",`+
		`"type":"server_error","code":"upstream_unavailable"}}`, body)

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.OutcomeTransportError, u.Outcome)
	assert.Zero(t, u.Status)
}

// Scenario: a request the gateway itself refuses. Each gets OpenAI's error
// shape with its status, the provider is not called, and nothing is recorded.
func TestTheGatewayRefusesWhatItCannotServe(t *testing.T) {
	t.Parallel()

	upstream, got := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	_, other, err := g.svc.AddKey(t.Context(), "laptop", []string{"another-model"})
	require.NoError(t, err)

	cases := map[string]struct {
		key, body string
		status    int
		code      string
	}{
		"no key":                      {"", `{"model":"gpt"}`, http.StatusUnauthorized, "missing_api_key"},
		"an unknown key":              {"sk-wrong", `{"model":"gpt"}`, http.StatusUnauthorized, "invalid_api_key"},
		"a model the key may not use": {other, `{"model":"gpt"}`, http.StatusForbidden, "model_not_allowed"},
		"a model nobody declared": {
			other, `{"model":"another-model"}`, http.StatusBadRequest, "model_not_found",
		},
		"a body that is not JSON":  {g.key, `model=gpt`, http.StatusBadRequest, "invalid_request"},
		"a body without its model": {g.key, `{"messages":[]}`, http.StatusBadRequest, "invalid_request"},
		"a stream that is not a boolean": {
			g.key, `{"model":"gpt","stream":"yes"}`, http.StatusBadRequest, "invalid_request",
		},
	}

	for name, c := range cases {
		status, _, body := g.call(t, c.key, c.body)
		assert.Equal(t, c.status, status, name)

		var parsed struct {
			Error struct {
				Message, Type, Code string
			}
		}

		require.NoError(t, json.Unmarshal([]byte(body), &parsed), name)
		assert.Equal(t, c.code, parsed.Error.Code, name)
		assert.NotEmpty(t, parsed.Error.Message, name)
	}

	assert.Zero(t, got.calls())

	rows, err := g.svc.ListUsage(t.Context(), 10)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// Scenario: a provider that reports no usage. The record holds rough counts
// from the sizes sent and received and says they are estimates, for a plain
// answer and for a stream.
func TestUsageIsEstimatedWhenTheProviderReportsNone(t *testing.T) {
	t.Parallel()

	silent, _ := provider(t, plain(http.StatusOK, `{"choices":[{"message":{"content":"hello there"}}]}`))
	g := newChatGateway(t, modelgateway.Config{}, silent)

	request := `{"model":"gpt","messages":[{"role":"user","content":"a question of some length"}]}`

	_, _, body := g.call(t, g.key, request)

	u := g.lastUsage(t)
	assert.True(t, u.Tokens.Estimated)
	assert.Equal(t, int64(len(request)/4), u.Tokens.Prompt)
	assert.Equal(t, int64(len(body)/4), u.Tokens.Completion)

	silentStream, _ := provider(t, stream(frame(chunkText), frame(chunkFinish), frameDone))
	s := newChatGateway(t, modelgateway.Config{}, silentStream)

	s.call(t, s.key, `{"model":"gpt","stream":true}`)

	u = s.lastUsage(t)
	assert.True(t, u.Tokens.Estimated)
	assert.Positive(t, u.Tokens.Prompt)
	assert.Equal(t, int64((len(chunkText)+len(chunkFinish))/4), u.Tokens.Completion)
}

// Scenario: the provider's stream stops without a finish reason or an end
// mark (it crashed), or goes silent. The client is told the answer ended
// early instead of taking half an answer for a whole one, and the record says
// truncated.
func TestAStreamThatEndsEarlyIsReportedAsTruncated(t *testing.T) {
	t.Parallel()

	crashed, _ := provider(t, stream(frame(chunkRole), frame(chunkText)))
	g := newChatGateway(t, modelgateway.Config{}, crashed)

	status, _, body := g.call(t, g.key, `{"model":"gpt","stream":true}`)
	assert.Equal(t, http.StatusOK, status)
	assert.True(t, strings.HasPrefix(body, frame(chunkRole)+frame(chunkText)), body)
	assert.Contains(t, body, `"code":"upstream_truncated"`)
	assert.NotContains(t, body, "[DONE]")
	assert.Equal(t, modelgateway.OutcomeTruncated, g.lastUsage(t).Outcome)

	// After one frame the provider says nothing more, until the gateway
	// gives up on it and closes the connection.
	silent, _ := provider(t, func(w http.ResponseWriter, r *http.Request) {
		stream(frame(chunkText))(w, r)
		<-r.Context().Done()
	})
	quick := newChatGateway(t, modelgateway.Config{StreamIdleTimeout: 150 * time.Millisecond}, silent)

	started := time.Now()
	_, _, body = quick.call(t, quick.key, `{"model":"gpt","stream":true}`)

	assert.Less(t, time.Since(started), 5*time.Second, "a silent provider must not hold the client")
	assert.Contains(t, body, `"code":"upstream_truncated"`)
	assert.Equal(t, modelgateway.OutcomeTruncated, quick.lastUsage(t).Outcome)
}

// Scenario: a provider closes right after its finish reason, without the end
// mark. The answer is whole, so the client gets the end mark from the gateway.
func TestAStreamThatFinishedWithoutItsEndMarkIsWhole(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, stream(frame(chunkText), frame(chunkFinish)))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	_, _, body := g.call(t, g.key, `{"model":"gpt","stream":true}`)
	assert.Equal(t, frame(chunkText)+frame(chunkFinish)+frameDone, body)
	assert.Equal(t, modelgateway.OutcomeOK, g.lastUsage(t).Outcome)
}

// Scenario: a stream was asked for and the provider answers with plain JSON
// (some compatible providers do for short answers or errors with status 200).
// The client gets that JSON, not an event stream wrapped around it.
func TestAPlainAnswerToAStreamRequestIsPassedAsPlain(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	_, headers, body := g.call(t, g.key, `{"model":"gpt","stream":true}`)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded
	assert.Equal(t, modelgateway.Tokens{Prompt: 12, Completion: 5, Cached: 4, Reasoning: 2}, g.lastUsage(t).Tokens)
}
