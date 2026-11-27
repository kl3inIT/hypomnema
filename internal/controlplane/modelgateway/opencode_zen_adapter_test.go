package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// zenWires is the wire the Console list names for each model of the test's
// Zen provider.
var zenWires = map[string]string{
	"glm-5.3": "chat-completions", "claude-sonnet-5-5": "messages", "gpt-6.1-sol": "responses",
}

// newZenGateway returns a gateway serving the built-in OpenCode Zen provider,
// seeded by its migration and given three models, one on each wire, beside
// the OpenCode Go provider, each provider's inference root its own stand-in.
func newZenGateway(t *testing.T, zen, goRoot *goBackend, subs modelgateway.Subscriptions) chatGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	seeded, err := orch.Profiles().GetProvider(t.Context(), "opencode-zen")
	require.NoError(t, err, "the migration seeds the provider")
	assert.Equal(t, profilestore.ProviderOpenCodeZen, seeded.Kind)

	_, err = orch.Profiles().ApplyProvider(t.Context(), "",
		"name: opencode-zen\nkind: opencode_zen\nmodels: [glm-5.3, claude-sonnet-5-5, gpt-6.1-sol]\n")
	require.NoError(t, err)
	_, err = orch.Profiles().ApplyProvider(t.Context(), "",
		"name: opencode-go\nkind: opencode_go\nmodels: [glm-5.3, minimax-m3, gpt-6-luna]\n")
	require.NoError(t, err)

	svc := modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
			p, err := orch.Profiles().GetProvider(ctx, name)
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
			switch kind {
			case profilestore.ProviderOpenCodeZen:
				wire, ok := zenWires[upstream]

				return wire, ok
			case profilestore.ProviderOpenCodeGo:
				wire, ok := goWires[upstream]

				return wire, ok
			default:
				return "", false
			}
		})

	_, key, err := svc.AddKey(t.Context(), "pi", []string{modelgateway.AllModels}, nil)
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"),
		modelgateway.Config{Subscriptions: subs, OpenCodeGoBaseURL: goRoot.URL, OpenCodeZenBaseURL: zen.URL}).
		Register(mux)

	return chatGateway{svc: svc, mux: mux, key: key}
}

const askZen = `{"model":"opencode-zen/%s","messages":[{"role":"user","content":"hi"}],"stream":true}`

// chatHi is a chat stream that says hi.
var chatHi = stream(
	"data: "+`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n",
	"data: [DONE]\n\n")

// byRuntime is the accounts of each runtime apart, as the control plane keeps
// them: a Go key resting does not rest a Zen key.
type byRuntime struct {
	mu       sync.Mutex
	runtimes map[string]*accounts
	refused  []modelgateway.AccountToken
}

func newByRuntime(ids ...string) *byRuntime {
	return &byRuntime{runtimes: map[string]*accounts{
		orchestration.OpenCodeGoRuntime:  {ids: ids},
		orchestration.OpenCodeZenRuntime: {ids: ids},
	}}
}

func (b *byRuntime) of(runtime string) *accounts {
	a, ok := b.runtimes[runtime]
	if !ok {
		panic("no accounts of runtime " + runtime)
	}

	return a
}

func (b *byRuntime) Token(
	ctx context.Context, runtime, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	return b.of(runtime).Token(ctx, runtime, session, skip...)
}

func (b *byRuntime) FirstFree(ctx context.Context, runtime string, reset func(string) time.Time) (time.Time, error) {
	return b.of(runtime).FirstFree(ctx, runtime, reset)
}

func (b *byRuntime) TokenAfterRefusal(context.Context, modelgateway.AccountToken) (modelgateway.AccountToken, error) {
	return modelgateway.AccountToken{}, modelgateway.ErrTokenUnavailable
}

func (b *byRuntime) MarkTokenRefused(_ context.Context, token modelgateway.AccountToken) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refused = append(b.refused, token)

	return nil
}

func (b *byRuntime) CoolDown(ctx context.Context, runtime, id string, until time.Time, reason string) error {
	return b.of(runtime).CoolDown(ctx, runtime, id, until, reason)
}

func (b *byRuntime) Quota(context.Context, string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

func (b *byRuntime) NoteQuota(string, float64) {}

func (b *byRuntime) NoteAnswer(string, string, http.Header) {}

// rest is until when an account of a runtime rests, and why.
func (b *byRuntime) rest(runtime, id string) (time.Time, string) {
	a := b.of(runtime)

	a.mu.Lock()
	defer a.mu.Unlock()

	return a.cooled[id], a.reason
}

// Scenario: pi chats with three Zen models. Each is called on the wire the
// Console list names for it, at OpenCode's inference root and not Go's:
// GLM on OpenAI's chat with a Bearer key, Claude on Anthropic's Messages with
// x-api-key and the API version, the GPT model on Responses. OpenCode is sent
// the gateway's own name and a session hash, never an OpenCode identity.
func TestEachZenModelIsCalledOnItsOwnWireAtTheInferenceRoot(t *testing.T) {
	t.Parallel()

	zen := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openai/v1/chat/completions":
			stream(
				"data: "+`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},`+
					`"finish_reason":"stop"}]}`+"\n\n",
				"data: [DONE]\n\n")(w, r)
		case "/anthropic/v1/messages":
			stream(claudeHello...)(w, r)
		case "/openai/v1/responses":
			stream(helloEvents...)(w, r)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	goRoot := newGoBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Go is not Zen", http.StatusTeapot)
	})
	g := newZenGateway(t, zen, goRoot, &accounts{})

	for model, want := range map[string]struct{ path, auth, apiKey, version string }{
		"glm-5.3":           {"/openai/v1/chat/completions", "Bearer access-0", "", ""},
		"claude-sonnet-5-5": {"/anthropic/v1/messages", "", "access-0", "2023-06-01"},
		"gpt-6.1-sol":       {"/openai/v1/responses", "Bearer access-0", "", ""},
	} {
		status, _, body := g.call(t, g.key, fmt.Sprintf(askZen, model))
		require.Equal(t, http.StatusOK, status, "%s: %s", model, body)
		assert.Equal(t, want.path, zen.lastPath(t), model)
		assert.Contains(t, strings.Join(dataLines(body), ""), "hel", model)

		headers, sent := zen.got.last(t)
		assert.Equal(t, want.auth, headers.Get("Authorization"), model)
		assert.Equal(t, want.apiKey, headers.Get("X-Api-Key"), model)
		assert.Equal(t, want.version, headers.Get("Anthropic-Version"), model)
		assert.Equal(t, "hypomnema/"+platform.Version, headers.Get("User-Agent"), model)
		assert.NotEmpty(t, headers.Get("X-Opencode-Session"), model)

		var request map[string]any
		require.NoError(t, json.Unmarshal([]byte(sent), &request))
		assert.Equal(t, model, request["model"], "the model's name at Zen")
	}

	assert.Zero(t, goRoot.got.calls(), "Go's root is not called for Zen")

	u := g.lastUsage(t)
	assert.True(t, strings.HasPrefix(u.Model, "opencode-zen/"), u.Model)
}

// Scenario: Claude Code asks a Zen model served on Messages through
// /v1/messages, and the Codex CLI a Zen model served on Responses through
// /v1/responses; each body is passed on with the model's name at Zen.
func TestZenPassesARouteItsOwnWire(t *testing.T) {
	t.Parallel()

	zen := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/anthropic/v1/messages":
			stream(claudeHello...)(w, r)
		case "/openai/v1/responses":
			stream(helloEvents...)(w, r)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	g := newZenGateway(t, zen, newGoBackend(t, chatHi), &accounts{})

	status, _, body := g.send(t, "/v1/messages",
		`{"model":"opencode-zen/claude-sonnet-5-5","max_tokens":64,"stream":true,`+
			`"messages":[{"role":"user","content":"hi"}]}`,
		claudeCodeHeaders(g.key)...)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "/anthropic/v1/messages", zen.lastPath(t))
	assert.Contains(t, body, "message_stop")

	_, sent := zen.got.last(t)
	assert.JSONEq(t, `{"model":"claude-sonnet-5-5","max_tokens":64,"stream":true,`+
		`"messages":[{"role":"user","content":"hi"}]}`, sent)

	status, _, body = g.respond(t, g.key, `{"model":"opencode-zen/gpt-6.1-sol","input":"hi"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "/openai/v1/responses", zen.lastPath(t))

	_, sent = zen.got.last(t)
	assert.JSONEq(t, `{"model":"gpt-6.1-sol","input":"hi","stream":true}`, sent)
}

// Scenario: the first Zen key's workspace has no balance left, or has reached
// its budget. Zen answers 402 or one of OpenCode's quota codes; that key rests
// for an hour, the request moves to the second key at once, which serves it,
// and no Go key rests. OpenCode sends its caps as 402 or 429 (opencode
// provider-error.ts QUOTA_CODES), or 403, which also move to the next key.
func TestAZenKeyWithNoBalanceRestsAnHourAndTheNextKeyServes(t *testing.T) {
	t.Parallel()

	for name, refusal := range map[string]struct {
		status     int
		body       string
		retryAfter string
	}{
		"402": {http.StatusPaymentRequired, `{"error":{"message":"Insufficient balance"}}`, ""},
		// Review of PR #1000: a quota code on a 400 moves on as a 402 does, and a quota rest is an hour whatever
		// Retry-After says.
		"400 insufficient_quota": {http.StatusBadRequest, `{"error":{"code":"insufficient_quota"}}`, ""},
		"402, Retry-After 30":    {http.StatusPaymentRequired, `{"error":{"message":"Insufficient balance"}}`, "30"},
		"creditlimitexceeded": {
			http.StatusTooManyRequests, `{"type":"error","error":{"type":"CreditLimitExceeded"}}`, "",
		},
		"budget, labeled": {http.StatusTooManyRequests,
			`{"error":{"message":"OpenCode: [budgetlimiterror] the workspace's monthly budget is reached"}}`, ""},
		"insufficient_quota": {http.StatusForbidden, `{"error":{"code":"insufficient_quota","message":"no"}}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			zen := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer access-0" {
					if refusal.retryAfter != "" {
						w.Header().Set("Retry-After", refusal.retryAfter)
					}

					plain(refusal.status, refusal.body)(w, r)

					return
				}

				chatHi(w, r)
			})
			subs := newByRuntime("acc_1", "acc_2")
			g := newZenGateway(t, zen, newGoBackend(t, chatHi), subs)

			status, _, body := g.call(t, g.key, fmt.Sprintf(askZen, "glm-5.3"))
			require.Equal(t, http.StatusOK, status, body)
			assert.Equal(t, 2, zen.got.calls(), "each key once")

			headers, _ := zen.got.last(t)
			assert.Equal(t, "Bearer acc_2-access-0", headers.Get("Authorization"))

			rest, reason := subs.rest(orchestration.OpenCodeZenRuntime, "acc_1")
			assert.WithinDuration(t, time.Now().Add(time.Hour), rest, time.Minute, "the key rests an hour")
			assert.Equal(t, "OpenCode Zen balance or budget used up", reason)

			goRest, _ := subs.rest(orchestration.OpenCodeGoRuntime, "acc_1")
			assert.True(t, goRest.IsZero(), "the Go key of the same id does not rest")
		})
	}
}

// Scenario: Zen answers 401 to the first key. That key is marked refused and
// the next serves; a refusal that is no quota rests the key only as long as
// any refusal does.
func TestZenMarksA401AndTriesTheNextKey(t *testing.T) {
	t.Parallel()

	zen := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-0" {
			plain(http.StatusUnauthorized, `{"error":{"message":"invalid key"}}`)(w, r)

			return
		}

		chatHi(w, r)
	})
	subs := newByRuntime("acc_1", "acc_2")
	g := newZenGateway(t, zen, newGoBackend(t, chatHi), subs)

	status, _, body := g.call(t, g.key, fmt.Sprintf(askZen, "glm-5.3"))
	require.Equal(t, http.StatusOK, status, body)
	require.Len(t, subs.refused, 1)
	assert.Equal(t, "acc_1", subs.refused[0].AccountID)

	rest, reason := subs.rest(orchestration.OpenCodeZenRuntime, "acc_1")
	assert.WithinDuration(t, time.Now().Add(2*time.Minute), rest, 30*time.Second, "a refused key's usual rest")
	assert.Equal(t, "OpenCode Zen refused the key", reason)
}

// Scenario: the Go key's monthly allowance is used up. It rests until Go's
// reset, and a Zen request is still served by the Zen key.
func TestAGoMonthlyLimitDoesNotRestTheZenKey(t *testing.T) {
	t.Parallel()

	goRoot := newGoBackend(t, plain(http.StatusTooManyRequests,
		`{"error":{"type":"GoUsageLimitError","message":"Monthly usage limit reached. Resets in 13 days"}}`))
	zen := newGoBackend(t, chatHi)
	subs := newByRuntime("acc_1")
	g := newZenGateway(t, zen, goRoot, subs)

	status, _, body := g.call(t, g.key, fmt.Sprintf(askGo, "glm-5.3"))
	require.Equal(t, http.StatusTooManyRequests, status, body)

	goRest, _ := subs.rest(orchestration.OpenCodeGoRuntime, "acc_1")
	assert.WithinDuration(t, time.Now().Add(13*24*time.Hour), goRest, time.Minute)

	status, _, body = g.call(t, g.key, fmt.Sprintf(askZen, "glm-5.3"))
	require.Equal(t, http.StatusOK, status, body)

	zenRest, _ := subs.rest(orchestration.OpenCodeZenRuntime, "acc_1")
	assert.True(t, zenRest.IsZero(), "the Zen key does not rest")
}

// Scenario: Zen's refusals, as opencode reads them. A 402 or a quota code in
// the body's code fields or its message's [code] label is a used-up balance
// or budget; a throttle, a plain refusal or Go's own limit is not.
func TestZenQuotaIsReadFromTheRefusal(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		status int
		body   string
		usedUp bool
	}{
		"402 with any body":       {402, `nope`, true},
		"error.code":              {400, `{"error":{"code":"insufficient_quota"}}`, true},
		"error.type, any case":    {429, `{"error":{"type":"CreditLimitExceeded"}}`, true},
		"top-level code":          {429, `{"code":"billing_error"}`, true},
		"usage_not_included":      {403, `{"error":{"type":"usage_not_included"}}`, true},
		"labeled message":         {400, `{"error":{"message":"Zen: [budgetlimiterror] reached"}}`, true},
		"labeled text":            {502, `Zen: [creditlimitexceeded] reached`, true},
		"a rate limit":            {429, `{"error":{"type":"rate_limit_error","message":"Too many requests"}}`, false},
		"a refused key":           {401, `{"error":{"type":"authentication_error"}}`, false},
		"Go's limit is not Zen's": {429, `{"error":{"type":"GoUsageLimitError"}}`, false},
	} {
		assert.Equal(t, c.usedUp, modelgateway.ZenQuotaUsedUp(c.status, []byte(c.body)), name)
	}
}

// Scenario: a client names a subscription's model without its provider (issue #1002). The gateway serves a
// subscription's models only as <provider>/<model>, or under a name of its own table, as LiteLLM's proxy serves a
// model_name and 9router an alias: a bare name reaches no provider on any wire, and the same name with its provider
// is served on each (TestEachZenModelIsCalledOnItsOwnWireAtTheInferenceRoot, TestZenPassesARouteItsOwnWire).
func TestABareModelNameReachesNoProvider(t *testing.T) {
	t.Parallel()

	zen := newGoBackend(t, chatHi)
	g := newZenGateway(t, zen, newGoBackend(t, chatHi), &accounts{})

	status, _, body := g.call(t, g.key, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, http.StatusBadRequest, status, "chat: %s", body)
	assert.Contains(t, body, "glm-5.3")

	status, _, body = g.send(t, "/v1/messages",
		`{"model":"claude-sonnet-5-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
		claudeCodeHeaders(g.key)...)
	assert.Equal(t, http.StatusNotFound, status, "messages: %s", body)

	status, _, body = g.respond(t, g.key, `{"model":"gpt-6.1-sol","input":"hi"}`)
	assert.Equal(t, http.StatusBadRequest, status, "responses: %s", body)

	assert.Zero(t, zen.got.calls(), "no provider is asked")
}

// zenRosters are accounts whose keys are of different workspaces, each served the models its own Console list names
// (modelgateway.ModelTokens).
type zenRosters struct {
	accounts

	served map[string][]string
}

func (a *zenRosters) TokenFor(
	ctx context.Context, runtime, model, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	var others []string

	for _, id := range a.ids {
		if !slices.Contains(a.served[id], model) {
			others = append(others, id)
		}
	}

	if len(others) == len(a.ids) {
		return modelgateway.AccountToken{}, modelgateway.ErrModelNotServed
	}

	return a.Token(ctx, runtime, session, append(skip, others...)...)
}

func (a *zenRosters) TokenAfterRefusalFor(
	ctx context.Context, refused modelgateway.AccountToken, model string,
) (modelgateway.AccountToken, error) {
	if !slices.Contains(a.served[refused.AccountID], model) {
		return modelgateway.AccountToken{}, modelgateway.ErrModelNotServed
	}

	return a.TokenAfterRefusal(ctx, refused)
}

func (a *zenRosters) Serves(_ context.Context, accountID, model string) bool {
	return slices.Contains(a.served[accountID], model)
}

// Scenario (review of PR #1000): two Zen keys of two workspaces. The first key's workspace has not enabled glm-5.3,
// and Zen answers it 404; the second's has. A request for glm-5.3 goes to the second key alone, as a Codex model
// goes only to the accounts whose list has it (issue #814).
func TestAZenModelGoesToAKeyWhoseWorkspaceHasIt(t *testing.T) {
	t.Parallel()

	zen := newGoBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-0" {
			plain(http.StatusNotFound, `{"error":{"type":"ModelError","message":"Model glm-5.3 not supported"}}`)(w, r)

			return
		}

		chatHi(w, r)
	})
	subs := &zenRosters{
		accounts: accounts{ids: []string{"acc_1", "acc_2"}},
		served:   map[string][]string{"acc_1": {"gpt-6.1-sol"}, "acc_2": {"glm-5.3", "gpt-6.1-sol"}},
	}
	g := newZenGateway(t, zen, newGoBackend(t, chatHi), subs)

	status, _, body := g.call(t, g.key, fmt.Sprintf(askZen, "glm-5.3"))
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 1, zen.got.calls())

	headers, _ := zen.got.last(t)
	assert.Equal(t, "Bearer acc_2-access-0", headers.Get("Authorization"))
}
