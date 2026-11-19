package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// goWires is the wire Go's model list names for each model of the test's provider.
var goWires = map[string]string{"glm-5.3": "chat-completions", "minimax-m3": "messages", "gpt-6-luna": "responses"}

// goBackend stands in for https://opencode.ai/inference/go: it records each
// request with its path and answers with handle.
type goBackend struct {
	*httptest.Server

	mu    sync.Mutex
	paths []string
	got   seen
}

func newGoBackend(t *testing.T, handle http.HandlerFunc) *goBackend {
	t.Helper()

	b := &goBackend{}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.URL.Path)
		b.mu.Unlock()

		b.got.add(r)
		handle(w, r)
	}))
	t.Cleanup(b.Close)

	return b
}

func (b *goBackend) lastPath(t *testing.T) string {
	t.Helper()

	b.mu.Lock()
	defer b.mu.Unlock()

	require.NotEmpty(t, b.paths, "Go was not called")

	return b.paths[len(b.paths)-1]
}

// newGoGateway returns a gateway serving the built-in OpenCode Go provider,
// seeded by its migration and given three models, one on each wire, and a key
// that may use every model.
func newGoGateway(t *testing.T, backend *goBackend, subs modelgateway.Subscriptions) chatGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	seeded, err := orch.GetProvider(t.Context(), "opencode-go")
	require.NoError(t, err, "the migration seeds the provider")
	assert.Equal(t, orchestration.ProviderOpenCodeGo, seeded.Kind)

	_, err = orch.ApplyProvider(t.Context(), "",
		"name: opencode-go\nkind: opencode_go\nmodels: [glm-5.3, minimax-m3, gpt-6-luna]\n")
	require.NoError(t, err)

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
		func(_ context.Context, ref string) (string, error) { return "sk-of-" + ref, nil }).
		WithKeyVault(testKeyVault(t, db)).
		WithServedWire(func(_ context.Context, kind, upstream string) (string, bool) {
			wire, ok := goWires[upstream]

			return wire, ok && kind == orchestration.ProviderOpenCodeGo
		})

	_, key, err := svc.AddKey(t.Context(), "pi", []string{modelgateway.AllModels}, nil)
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"),
		modelgateway.Config{Subscriptions: subs, OpenCodeGoBaseURL: backend.URL}).Register(mux)

	return chatGateway{svc: svc, mux: mux, key: key}
}

const askGo = `{"model":"opencode-go/%s","messages":[{"role":"user","content":"hi"}],"stream":true}`

// Scenario: pi chats with three Go models. Each is called on the wire Go's
// list names for it, under the key's account: GLM on OpenAI's chat with a
// Bearer key, MiniMax on Anthropic's Messages with x-api-key and the API
// version, the GPT model on Responses; pi gets OpenAI's chunks from each.
func TestEachGoModelIsCalledOnItsOwnWire(t *testing.T) {
	t.Parallel()

	backend := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			stream(
				"data: "+`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},`+
					`"finish_reason":"stop"}]}`+"\n\n",
				"data: "+`{"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,`+
					`"completion_tokens":1,"total_tokens":4}}`+"\n\n",
				"data: [DONE]\n\n")(w, r)
		case "/anthropic/v1/messages":
			stream(claudeHello...)(w, r)
		case "/openai/v1/responses":
			stream(helloEvents...)(w, r)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	g := newGoGateway(t, backend, &accounts{})

	for model, want := range map[string]struct{ path, auth, apiKey, version string }{
		"glm-5.3":    {"/openai/v1/chat/completions", "Bearer access-0", "", ""},
		"minimax-m3": {"/anthropic/v1/messages", "", "access-0", "2023-06-01"},
		"gpt-6-luna": {"/openai/v1/responses", "Bearer access-0", "", ""},
	} {
		status, _, body := g.call(t, g.key, fmt.Sprintf(askGo, model))
		require.Equal(t, http.StatusOK, status, "%s: %s", model, body)
		assert.Equal(t, want.path, backend.lastPath(t), model)
		assert.Contains(t, strings.Join(dataLines(body), ""), "hel", model)

		headers, sent := backend.got.last(t)
		assert.Equal(t, want.auth, headers.Get("Authorization"), model)
		assert.Equal(t, want.apiKey, headers.Get("X-Api-Key"), model)
		assert.Equal(t, want.version, headers.Get("Anthropic-Version"), model)
		assert.Empty(t, headers.Get("X-Client-Header"), "a client header never reaches Go")

		var request map[string]any
		require.NoError(t, json.Unmarshal([]byte(sent), &request))
		assert.Equal(t, model, request["model"], "the model's name at Go")

		if model == "minimax-m3" {
			assert.NotContains(t, sent, "x-anthropic-billing-header", "Go gets no Claude Code disguise")
			assert.Nil(t, request["metadata"])
		}
	}
}

// Scenario: two requests of one conversation, and one of another key. Go is
// sent the gateway's own name as User-Agent and a stable x-opencode-session:
// the same for the conversation, another for the other client, and never the
// client's key or cache key as it was sent.
func TestGoIsSentAStableSessionPerConversation(t *testing.T) {
	t.Parallel()

	backend := newGoBackend(t, stream(
		"data: "+`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n",
		"data: [DONE]\n\n"))
	g := newGoGateway(t, backend, &accounts{})

	session := func(body string) string {
		t.Helper()

		status, _, answer := g.call(t, g.key, body)
		require.Equal(t, http.StatusOK, status, answer)

		headers, _ := backend.got.last(t)
		assert.Equal(t, "hypomnema/"+platform.Version, headers.Get("User-Agent"))

		return headers.Get("X-Opencode-Session")
	}

	first := session(fmt.Sprintf(askGo, "glm-5.3"))
	require.NotEmpty(t, first)
	assert.Equal(t, first, session(fmt.Sprintf(askGo, "glm-5.3")), "one client keeps one session")

	cached := `{"model":"opencode-go/glm-5.3","prompt_cache_key":"chat-42","messages":[{"role":"user","content":"hi"}]}`
	other := session(cached)
	assert.NotEqual(t, first, other, "a conversation of its own")
	assert.NotEqual(t, "chat-42", other, "the client's key is hashed")
	assert.Equal(t, other, session(cached))
}

// Scenario: the first Go key's monthly allowance is used up. Go answers 429
// "Monthly usage limit reached. Resets in 13 days"; that key rests until then,
// the request moves to the second key at once, which serves it, and the first
// key is not asked again while it rests.
func TestAUsedUpGoKeyRestsAndTheNextKeyServes(t *testing.T) {
	t.Parallel()

	const usedUp = `{"type":"error","error":{"type":"GoUsageLimitError","message":"[429] Monthly usage limit ` +
		`reached. Resets in 13 days. To continue using this model now, enable usage from your available balance"}}`

	backend := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-0" {
			plain(http.StatusTooManyRequests, usedUp)(w, r)

			return
		}

		stream("data: "+`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n",
			"data: [DONE]\n\n")(w, r)
	})
	subs := &accounts{ids: []string{"acc_1", "acc_2"}}
	g := newGoGateway(t, backend, subs)

	status, _, body := g.call(t, g.key, fmt.Sprintf(askGo, "glm-5.3"))
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 2, backend.got.calls(), "each key once")

	headers, _ := backend.got.last(t)
	assert.Equal(t, "Bearer acc_2-access-0", headers.Get("Authorization"))

	subs.mu.Lock()
	rest := subs.cooled["acc_1"]
	reason := subs.reason
	subs.mu.Unlock()

	assert.WithinDuration(t, time.Now().Add(13*24*time.Hour), rest, time.Minute,
		"the key rests until the reset Go named, past the two hours other resets are held to")
	assert.Equal(t, "OpenCode Go usage limit", reason)

	status, _, body = g.call(t, g.key, fmt.Sprintf(askGo, "glm-5.3"))
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 3, backend.got.calls(), "the resting key is not asked again")
}

// Scenario: Claude Code asks a Go model served on Messages through
// /v1/messages, and the Codex CLI a Go model served on Responses through
// /v1/responses. Each body is passed on as the client sent it, under the
// model's name at Go, and the answer comes back in the client's own format.
func TestGoPassesARouteItsOwnWire(t *testing.T) {
	t.Parallel()

	backend := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anthropic/v1/messages":
			stream(claudeHello...)(w, r)
		case "/openai/v1/responses":
			stream(helloEvents...)(w, r)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	g := newGoGateway(t, backend, &accounts{})

	status, _, body := g.send(t, "/v1/messages",
		`{"model":"opencode-go/minimax-m3","max_tokens":64,"stream":true,"kept":1,`+
			`"messages":[{"role":"user","content":"hi"}]}`,
		claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "/anthropic/v1/messages", backend.lastPath(t))
	assert.Contains(t, body, "message_stop", "the Messages stream as Go sent it")

	headers, sent := backend.got.last(t)
	assert.Equal(t, "access-0", headers.Get("X-Api-Key"))
	assert.NotEmpty(t, headers.Get("X-Opencode-Session"))
	assert.JSONEq(t, `{"model":"minimax-m3","max_tokens":64,"stream":true,"kept":1,`+
		`"messages":[{"role":"user","content":"hi"}]}`, sent)

	status, _, body = g.respond(t, g.key, `{"model":"opencode-go/gpt-6-luna","input":"hi"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "/openai/v1/responses", backend.lastPath(t))
	assert.Contains(t, body, "response.completed")

	_, sent = backend.got.last(t)
	assert.JSONEq(t, `{"model":"gpt-6-luna","input":"hi","stream":true}`, sent)

	status, _, body = g.respond(t, g.key, `{"model":"opencode-go/glm-5.3","input":"hi"}`)
	assert.Equal(t, http.StatusBadRequest, status, "a chat-only Go model is not served on /v1/responses: %s", body)
}

// Scenario: Go's refusals, as omniroute and opencode read them. A 429 naming
// a monthly limit and its countdown is a used-up allowance until then; a
// GoUsageLimitError is one with no reset named; a plain 429 or another error
// is not.
func TestGoUsageLimitsAreReadFromTheRefusal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	for name, c := range map[string]struct {
		status int
		body   string
		usedUp bool
		reset  time.Duration
	}{
		"monthly, days": {429, `{"error":{"message":"Monthly usage limit reached. Resets in 13 days. To continue ` +
			`using this model now, enable usage from your available balance"}}`, true, 13 * 24 * time.Hour},
		"monthly, one hour":   {429, `"[429] Monthly usage limit reached. Resets in 1 hour."`, true, time.Hour},
		"typed, no countdown": {429, `{"error":{"type":"GoUsageLimitError","limitName":"5 hour"}}`, true, 0},
		"typed, other status": {402, `{"type":"GoUsageLimitError"}`, true, 0},
		"a rate limit":        {429, `{"error":{"message":"Too many requests"}}`, false, 0},
		"monthly but not 429": {400, `Monthly usage limit reached. Resets in 2 days`, false, 0},
	} {
		usedUp, reset := modelgateway.GoUsageLimit(c.status, []byte(c.body), now)
		assert.Equal(t, c.usedUp, usedUp, name)

		if c.reset == 0 {
			assert.True(t, reset.IsZero(), name)
		} else {
			assert.Equal(t, now.Add(c.reset), reset, name)
		}
	}
}
