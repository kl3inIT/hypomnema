package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// googleAccount is the Antigravity account logged in on the control plane.
type googleAccount struct {
	accounts
}

func (a *googleAccount) Token(
	ctx context.Context, runtime string, skip ...string,
) (modelgateway.AccountToken, error) {
	token, err := a.accounts.Token(ctx, runtime, skip...)
	token.Project = "bright-wave-1234"

	return token, err
}

func (a *googleAccount) parked() (time.Time, string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.cooling, a.reason
}

// cloudCode starts a stand-in for Google's Cloud Code backend.
func cloudCode(t *testing.T, handle http.HandlerFunc) (*httptest.Server, *seen) {
	t.Helper()

	got := &seen{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), http.StatusNotFound)

			return
		}

		got.add(r)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, got
}

// newAntigravityGateway returns a gateway whose models `flash` and `pro` are
// served from the built-in Antigravity provider, then from the fallbacks.
func newAntigravityGateway(
	t *testing.T, backend *httptest.Server, subs modelgateway.Subscriptions, fallbacks ...*httptest.Server,
) chatGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	flash := "name: flash\ntargets:\n  - {provider: antigravity-subscription, model: gemini-3.8-flash-high}\n"

	for i, p := range fallbacks {
		_, err := orch.ApplyProvider(t.Context(), "", fmt.Sprintf(
			"name: p%d\nkind: openai_compatible\nbase_url: %s/v1\napi_key_ref: vault:p%d\nmodels: [up]\n", i, p.URL, i))
		require.NoError(t, err)

		flash += fmt.Sprintf("  - {provider: p%d, model: up}\n", i)
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

	for _, model := range []string{
		flash, "name: pro\ntargets: [{provider: antigravity-subscription, model: gemini-3.1-pro-high}]\n",
	} {
		_, err = svc.ApplyModel(t.Context(), model)
		require.NoError(t, err, "the provider is built in: no one declared it")
	}

	_, key, err := svc.AddKey(t.Context(), "tool", nil)
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"), modelgateway.Config{
		Subscriptions: subs, AntigravityBaseURL: backend.URL, AntigravityManifestURL: backend.URL + "/manifest",
	}).Register(mux)

	return chatGateway{svc: svc, mux: mux, key: key}
}

var geminiEvents = []string{
	event(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}}`),
	event(`{"response":{"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":7,"thoughtsTokenCount":4,"totalTokenCount":41}}}`),
}

const askFlash = `{"model":"flash","messages":[{"role":"system","content":"Be brief."},` +
	`{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]`

// Scenario: a tool of the owner's asks for a model the gateway serves from
// the Antigravity account. The backend is called under the account's project
// with the IDE's user agent and none of Google's other headers, the request
// in Gemini's shape with the client's own tools and nothing else; the client
// gets OpenAI's chunks, or one completion when it does not stream; the tokens
// are recorded.
func TestAChatIsServedFromTheAntigravitySubscription(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, stream(geminiEvents...))
	g := newAntigravityGateway(t, backend, &googleAccount{})

	status, _, body := g.call(t, g.key, askFlash+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)

	sentHeaders, sent := got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	assert.Regexp(t, `^antigravity/hub/\d+\.\d+\.\d+ \(aidev_client; os_type=darwin; arch=arm64`,
		sentHeaders.Get("User-Agent"))
	assert.Empty(t, sentHeaders.Get("X-Goog-Api-Client"))
	assert.Empty(t, sentHeaders.Get("Client-Metadata"))
	assert.Empty(t, sentHeaders.Get("X-Client-Header"))

	var envelope struct {
		Project     string  `json:"project"`
		Model       string  `json:"model"`
		RequestType *string `json:"requestType"`
		Request     struct {
			Tools []struct {
				FunctionDeclarations []json.RawMessage `json:"functionDeclarations"`
			} `json:"tools"`
		} `json:"request"`
	}

	require.NoError(t, json.Unmarshal([]byte(sent), &envelope))
	assert.Equal(t, "bright-wave-1234", envelope.Project)
	assert.Equal(t, "gemini-3.8-flash-high", envelope.Model)
	assert.Nil(t, envelope.RequestType)
	require.Len(t, envelope.Request.Tools, 1)
	assert.Len(t, envelope.Request.Tools[0].FunctionDeclarations, 1, "the client's tool and no other")

	lines := dataLines(body)
	require.Len(t, lines, 4, body)
	assert.JSONEq(t, `{"role":"assistant","content":"hel"}`, deltaOf(t, lines[0]))
	assert.Contains(t, lines[2], `"finish_reason":"stop"`)
	assert.Equal(t, "[DONE]", lines[3])

	u := g.lastUsage(t)
	assert.Equal(t, "antigravity-subscription", u.Provider)
	assert.EqualValues(t, 30, u.Tokens.Prompt)
	assert.EqualValues(t, 11, u.Tokens.Completion)
	assert.EqualValues(t, 4, u.Tokens.Reasoning)

	status, headers, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Contains(t, body, `"content":"hello"`)
	assert.Contains(t, body, `"object":"chat.completion"`)
}

// Scenario: the account's quota for one model is used up. The client gets a
// 429 with the wait Google named; until then that model is not sent to the
// backend again, while another model of the same account still is. With a
// second target the model is served from it.
func TestAModelWhoseQuotaIsUsedUpWaitsAndTheOthersDoNot(t *testing.T) {
	t.Parallel()

	exhausted := plain(http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED",
	  "message":"You have exhausted your capacity on this model.","details":[
	  {"reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"2h0m0s"}}]}}`)

	var got *seen

	backend, requests := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if _, body := got.last(t); containsModel(body, "gemini-3.1-pro-high") {
			stream(geminiEvents...)(w, r)

			return
		}

		exhausted(w, r)
	})
	got = requests
	g := newAntigravityGateway(t, backend, &googleAccount{})

	status, headers, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, body, "exhausted your capacity")
	assert.Equal(t, "7201", headers.Get("Retry-After"))

	status, headers, _ = g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.NotEmpty(t, headers.Get("Retry-After"))
	assert.Equal(t, 1, got.calls(), "a model that waits is not sent to the backend")

	status, _, body = g.call(t, g.key, `{"model":"pro","messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, http.StatusOK, status, body)

	second, _ := provider(t, plain(http.StatusOK, answer))
	withFallback := newAntigravityGateway(t, backend, &googleAccount{}, second)

	status, _, body = withFallback.call(t, withFallback.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded
}

func containsModel(body, model string) bool {
	var envelope struct {
		Model string `json:"model"`
	}

	return json.Unmarshal([]byte(body), &envelope) == nil && envelope.Model == model
}

// Scenario: Google suspends the account for its terms, or has no capacity
// for the model. A suspended account is taken out of use for a day, with
// Google's reason and appeal link for the owner to read, and the request is
// served by the model's next target. A model with no capacity makes nothing
// wait: the next request goes to the backend again.
func TestASuspendedAccountIsTakenOutOfUseAndNoCapacityIsNot(t *testing.T) {
	t.Parallel()

	suspended, _ := cloudCode(t, plain(http.StatusForbidden, `{"error":{"code":403,"status":"PERMISSION_DENIED",
	  "message":"This service has been disabled in this account for violation of Terms of Service.","details":[
	  {"reason":"TOS_VIOLATION","metadata":{"appeal_url":"https://support.google.com/appeal"}}]}}`))
	account := &googleAccount{}
	second, _ := provider(t, plain(http.StatusOK, answer))
	g := newAntigravityGateway(t, suspended, account, second)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded

	until, reason := account.parked()
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), until, time.Minute)
	assert.Contains(t, reason, "suspended by Google")
	assert.Contains(t, reason, "https://support.google.com/appeal")

	alone := newAntigravityGateway(t, suspended, &googleAccount{})

	status, _, body = alone.call(t, alone.key, askFlash+`}`)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, "account_suspended")

	busy, got := cloudCode(t, plain(http.StatusServiceUnavailable, `{"error":{"code":503,"status":"UNAVAILABLE",
	  "message":"No capacity available for model","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`))
	crowded := newAntigravityGateway(t, busy, &googleAccount{})

	for range 2 {
		status, _, body = crowded.call(t, crowded.key, askFlash+`}`)
		assert.Equal(t, http.StatusServiceUnavailable, status)
		assert.Contains(t, body, "No capacity")
	}

	assert.Equal(t, 2, got.calls())
}

// Scenario: an agent fires many requests at once. Three run under the
// account; the fourth is refused at once with a 429 instead of being sent,
// and is accepted again once one of the three has ended.
func TestAnAccountRunsThreeRequestsAtOnce(t *testing.T) {
	t.Parallel()

	arrived, release := make(chan struct{}, 8), make(chan struct{})

	backend, got := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}

		select {
		case <-release:
		case <-r.Context().Done():
		}

		stream(geminiEvents...)(w, r)
	})
	g := newAntigravityGateway(t, backend, &googleAccount{})

	var running sync.WaitGroup

	for range 3 {
		running.Go(func() {
			status, _, body := g.call(t, g.key, askFlash+`}`)
			assert.Equal(t, http.StatusOK, status, body)
		})
	}

	for range 3 {
		<-arrived
	}

	status, headers, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, body, "requests in flight")
	assert.NotEmpty(t, headers.Get("Retry-After"))
	assert.Equal(t, 3, got.calls())

	close(release)
	running.Wait()

	status, _, body = g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status, body)
}

// Scenario: two Antigravity accounts, the first with a twentieth of its
// quota for the model left. Until the gateway has read the quota the first
// account serves; a successful request makes it ask, once; from then on the
// second account serves that model while it can, and the first is kept for
// when it cannot. A single account near its quota is still used.
func TestAnAccountNearItsQuotaIsUsedLast(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, stream(geminiEvents...))
	subs := &googleAccount{accounts: accounts{
		ids:   []string{"acc_1", "acc_2"},
		quota: map[string]map[string]float64{"acc_1": {"gemini-3.8-flash-high": 0.05}, "acc_2": {}},
	}}
	g := newAntigravityGateway(t, backend, subs)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	require.Eventually(t, func() bool { return subs.asked("acc_1") == 1 }, 5*time.Second, 10*time.Millisecond)

	// The reading is stored just after it is asked for.
	require.Eventually(t, func() bool {
		g.call(t, g.key, askFlash+`}`)
		h, _ := got.last(t)

		return h.Get("Authorization") == "Bearer acc_2-access-0"
	}, 5*time.Second, 20*time.Millisecond)

	for range 3 {
		g.call(t, g.key, askFlash+`}`)
	}

	assert.Equal(t, 1, subs.asked("acc_1"), "a quota is read at most every fifteen minutes")

	alone := &googleAccount{accounts: accounts{
		quota: map[string]map[string]float64{"acc_1": {"gemini-3.8-flash-high": 0}},
	}}
	single := newAntigravityGateway(t, backend, alone)

	single.call(t, single.key, askFlash+`}`)
	require.Eventually(t, func() bool { return alone.asked("acc_1") == 1 }, 5*time.Second, 10*time.Millisecond)

	status, _, body = single.call(t, single.key, askFlash+`}`)
	assert.Equal(t, http.StatusOK, status, body)
}
