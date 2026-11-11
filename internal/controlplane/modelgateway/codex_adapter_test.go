package modelgateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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

// accounts stands in for the accounts logged in on the control plane: one
// account unless ids names several, taken in that order. A token can be
// refused, and an account can cool down.
type accounts struct {
	mu sync.Mutex
	// none says no account is logged in.
	none bool
	// ids are the accounts, in order; empty means the one account "acc_1".
	ids []string
	// keepCooling makes a cooling account still be given, so a test can see
	// what its next failure does.
	keepCooling bool
	// quota is what the vendor says is left of each account's quota per model.
	quota map[string]map[string]float64

	refreshes int
	// cooling and reason are the last cooldown; cooled holds each account's,
	// and waits how long each one was, in order.
	cooling   time.Time
	reason    string
	cooled    map[string]time.Time
	waits     map[string][]time.Duration
	quotaAsks map[string]int
	// noted is what the gateway said is left of each account's whole quota.
	noted map[string]float64
}

func (a *accounts) Token(_ context.Context, _, _ string, skip ...string) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.none {
		return modelgateway.AccountToken{}, modelgateway.ErrNoAccount
	}

	ids := a.ids
	if len(ids) == 0 {
		ids = []string{"acc_1"}
	}

	var free time.Time

	for _, id := range ids {
		if slices.Contains(skip, id) {
			continue
		}

		if until := a.cooled[id]; until.After(time.Now()) && !a.keepCooling {
			if free.IsZero() || until.Before(free) {
				free = until
			}

			continue
		}

		token := "access-" + strconv.Itoa(a.refreshes)
		if id != "acc_1" {
			token = id + "-" + token
		}

		return modelgateway.AccountToken{
			AccountID: id, ExternalID: "chatgpt-account", AccessToken: token, Version: a.refreshes + 1,
		}, nil
	}

	if !free.IsZero() {
		return modelgateway.AccountToken{}, &modelgateway.CoolingError{Until: free}
	}

	return modelgateway.AccountToken{}, modelgateway.ErrNoAccount
}

func (a *accounts) TokenAfterRefusal(
	ctx context.Context, refused modelgateway.AccountToken,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	a.refreshes++
	a.mu.Unlock()

	// The same account, whatever comes before it.
	all := a.ids
	if len(all) == 0 {
		all = []string{"acc_1"}
	}

	others := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == refused.AccountID })

	return a.Token(ctx, "", "", others...)
}

func (a *accounts) CoolDown(_ context.Context, _, id string, until time.Time, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cooled == nil {
		a.cooled, a.waits = map[string]time.Time{}, map[string][]time.Duration{}
	}

	a.cooling, a.reason = until, reason
	a.cooled[id] = until
	a.waits[id] = append(a.waits[id], time.Until(until).Round(time.Second))

	return nil
}

func (a *accounts) Quota(_ context.Context, id string) (map[string]float64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.quotaAsks == nil {
		a.quotaAsks = map[string]int{}
	}

	a.quotaAsks[id]++

	return a.quota[id], nil
}

func (a *accounts) NoteQuota(id string, left float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.noted == nil {
		a.noted = map[string]float64{}
	}

	a.noted[id] = left
}

// quotaNoted is what was last said to be left of an account's whole quota.
func (a *accounts) quotaNoted(id string) (float64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	left, ok := a.noted[id]

	return left, ok
}

func (a *accounts) cooledUntil() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.cooling
}

// waited returns how long each cooldown of an account was, in order.
func (a *accounts) waited(id string) []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.waits[id])
}

func (a *accounts) asked(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.quotaAsks[id]
}

// codexBackend starts a stand-in for chatgpt.com/backend-api/codex that
// records what it gets and answers with handle.
func codexBackend(t *testing.T, handle http.HandlerFunc) (*httptest.Server, *seen) {
	t.Helper()

	got := &seen{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)

			return
		}

		got.add(r)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, got
}

// newCodexGateway returns a gateway whose model `sol` is served from the
// built-in Codex provider, then from the given OpenAI-compatible providers.
func newCodexGateway(
	t *testing.T, backend *httptest.Server, subs modelgateway.Subscriptions, fallbacks ...*httptest.Server,
) chatGateway {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	orch := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	model := "name: sol\ntargets:\n  - {provider: codex-subscription, model: gpt-6.1-sol}\n"

	for i, p := range fallbacks {
		_, err := orch.ApplyProvider(t.Context(), "", fmt.Sprintf(
			"name: p%d\nkind: openai_compatible\nbase_url: %s/v1\napi_key_ref: vault:p%d\nmodels: [up]\n", i, p.URL, i))
		require.NoError(t, err)

		model += fmt.Sprintf("  - {provider: p%d, model: up}\n", i)
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
	require.NoError(t, err, "the provider is built in: no one declared it")

	_, key, err := svc.AddKey(t.Context(), "pi", []string{"sol"})
	require.NoError(t, err)

	mux := http.NewServeMux()
	modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"),
		modelgateway.Config{Subscriptions: subs, CodexBaseURL: backend.URL}).Register(mux)

	return chatGateway{svc: svc, mux: mux, key: key}
}

func event(payload string) string { return "event: x\ndata: " + payload + "\n\n" }

var helloEvents = []string{
	event(`{"type":"response.created","response":{"id":"resp_1"}}`),
	event(`{"type":"response.output_text.delta","item_id":"msg_1","delta":"hel"}`),
	event(`{"type":"response.output_text.delta","item_id":"msg_1","delta":"lo"}`),
	event(`{"type":"response.completed","response":{"usage":{"input_tokens":30,"output_tokens":7,"total_tokens":37,` +
		`"input_tokens_details":{"cached_tokens":20},"output_tokens_details":{"reasoning_tokens":4}}}}`),
}

const askSol = `{"model":"sol","temperature":1,"messages":[{"role":"system","content":"Be brief."},` +
	`{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]`

// dataLines are the payloads of an event stream's data lines.
func dataLines(body string) []string {
	var out []string

	for line := range strings.SplitSeq(body, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			out = append(out, payload)
		}
	}

	return out
}

// Scenario: pi streams a chat with the model the gateway serves from the
// owner's Codex subscription. The backend is called as the Codex CLI calls
// it, under the account logged in on the control plane, with the request in
// the shape a gpt-6 model takes; pi gets OpenAI's chunks and the end mark,
// without the usage frame it did not ask for; the tokens are recorded.
func TestAChatIsServedFromTheCodexSubscription(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})

	status, headers, body := g.call(t, g.key, askSol+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/event-stream", headers.Get("Content-Type"))

	sentHeaders, sent := got.last(t)
	assert.Equal(t, "Bearer access-0", sentHeaders.Get("Authorization"))
	assert.Equal(t, "chatgpt-account", sentHeaders.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "codex_cli_rs", sentHeaders.Get("Originator"))
	assert.Regexp(t, `^codex_cli_rs/\d+\.\d+\.\d+$`, sentHeaders.Get("User-Agent"))
	assert.NotEmpty(t, sentHeaders.Get("Version"))
	assert.Equal(t, "true", sentHeaders.Get("X-Openai-Internal-Codex-Responses-Lite"))
	assert.Empty(t, sentHeaders.Get("X-Client-Header"))

	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(sent), &request))
	assert.Equal(t, "gpt-6.1-sol", request["model"])
	assert.Equal(t, true, request["stream"])
	assert.NotContains(t, request, "temperature")
	assert.Equal(t, sentHeaders.Get("Session_id"), request["prompt_cache_key"])
	assert.Len(t, request["input"], 3, "the tools, the instructions and the user's message")

	lines := dataLines(body)
	require.Len(t, lines, 4, body)
	assert.JSONEq(t, `{"role":"assistant","content":"hel"}`, deltaOf(t, lines[0]))
	assert.JSONEq(t, `{"content":"lo"}`, deltaOf(t, lines[1]))
	assert.Contains(t, lines[2], `"finish_reason":"stop"`)
	assert.Contains(t, lines[2], `"model":"sol"`)
	assert.Equal(t, "[DONE]", lines[3])

	u := g.lastUsage(t)
	assert.Equal(t, "codex-subscription", u.Provider)
	assert.Equal(t, "gpt-6.1-sol", u.UpstreamModel)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)
	assert.EqualValues(t, 30, u.Tokens.Prompt)
	assert.EqualValues(t, 20, u.Tokens.Cached)
	assert.EqualValues(t, 7, u.Tokens.Completion)
	assert.EqualValues(t, 4, u.Tokens.Reasoning)
	assert.False(t, u.Tokens.Estimated)

	_, _, body = g.call(t, g.key, askSol+`,"stream":true,"stream_options":{"include_usage":true}}`)
	lines = dataLines(body)
	require.Len(t, lines, 5, "a client that asks for the usage gets its frame")
	assert.Contains(t, lines[3], `"usage":{"prompt_tokens":30`)
}

func deltaOf(t *testing.T, chunk string) string {
	t.Helper()

	var parsed struct {
		Choices []struct {
			Delta json.RawMessage `json:"delta"`
		} `json:"choices"`
	}

	require.NoError(t, json.Unmarshal([]byte(chunk), &parsed), chunk)
	require.Len(t, parsed.Choices, 1, chunk)

	return string(parsed.Choices[0].Delta)
}

// Scenario: a client that does not stream. The backend only streams, so the
// gateway reads its whole answer and gives one chat completion.
func TestAClientThatDoesNotStreamGetsOneCompletion(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{})

	status, headers, body := g.call(t, g.key, askSol+`}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))

	var answer struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &answer), body)
	assert.Equal(t, "chat.completion", answer.Object)
	assert.Equal(t, "sol", answer.Model)
	require.Len(t, answer.Choices, 1)
	assert.Equal(t, "hello", answer.Choices[0].Message.Content)
	assert.Equal(t, "stop", answer.Choices[0].FinishReason)
	assert.EqualValues(t, 30, g.lastUsage(t).Tokens.Prompt)

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "text/event-stream", sentHeaders.Get("Accept"))
}

// Scenario: the access token ended between two refreshes. The backend
// answers 401 once; the account's tokens are refreshed and the request is
// sent again under the new one. The client sees only the answer.
func TestARefusedTokenIsRefreshedAndTheRequestSentAgain(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer access-0" {
			plain(http.StatusUnauthorized, `{"error":{"code":"token_expired"}}`)(w, r)

			return
		}

		stream(helloEvents...)(w, r)
	})
	subs := &accounts{}
	g := newCodexGateway(t, backend, subs)

	status, _, body := g.call(t, g.key, askSol+`}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 2, got.calls())
	assert.Equal(t, 1, subs.refreshes)

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer access-1", sentHeaders.Get("Authorization"))
}

// Scenario: the subscription's usage limit is reached. The account cools
// down until the time the backend names and the client gets a 429 with
// Retry-After; while it cools the backend is not called. With a second
// target the model is served from it instead.
func TestAUsageLimitCoolsTheAccountDownAndTheNextTargetServes(t *testing.T) {
	t.Parallel()

	resets := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	limit := plain(http.StatusTooManyRequests, fmt.Sprintf(
		`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":%d}}`,
		resets.Unix()))

	backend, got := codexBackend(t, limit)
	subs := &accounts{}
	g := newCodexGateway(t, backend, subs)

	status, headers, body := g.call(t, g.key, askSol+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, body, "usage_limit_reached")
	assert.NotEmpty(t, headers.Get("Retry-After"))
	assert.Equal(t, resets, subs.cooledUntil())

	status, headers, body = g.call(t, g.key, askSol+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, body, "account_unavailable")
	assert.NotEmpty(t, headers.Get("Retry-After"))
	assert.Equal(t, 1, got.calls(), "a cooling account is not sent to the backend")

	second, secondGot := provider(t, plain(http.StatusOK, answer))
	withFallback := newCodexGateway(t, backend, &accounts{}, second)

	status, _, body = withFallback.call(t, withFallback.key, askSol+`}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded
	assert.Equal(t, 1, secondGot.calls())
	assert.Equal(t, 2, withFallback.lastUsage(t).Attempts)
}

// Scenario: the backend accepts the request and then fails inside its
// stream. Before any text, the client gets an error status, or the next
// target's answer. After some text, the stream ends with the gateway's
// notice that the answer was cut, and no end mark.
func TestAFailureInsideTheStream(t *testing.T) {
	t.Parallel()

	early, _ := codexBackend(t, stream(
		event(`{"type":"response.created","response":{}}`),
		event(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"The model is overloaded"}}}`)))
	g := newCodexGateway(t, early, &accounts{})

	status, _, body := g.call(t, g.key, askSol+`,"stream":true}`)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, body, "The model is overloaded")
	assert.Equal(t, modelgateway.OutcomeUpstreamError, g.lastUsage(t).Outcome)

	second, _ := provider(t, plain(http.StatusOK, answer))
	withFallback := newCodexGateway(t, early, &accounts{}, second)

	status, _, body = withFallback.call(t, withFallback.key, askSol+`,"stream":true}`)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded

	late, _ := codexBackend(t, stream(helloEvents[1],
		event(`{"type":"error","code":"server_error","message":"lost"}`)))
	cut := newCodexGateway(t, late, &accounts{})

	status, _, body = cut.call(t, cut.key, askSol+`,"stream":true}`)
	assert.Equal(t, http.StatusOK, status)

	lines := dataLines(body)
	require.Len(t, lines, 2, body)
	assert.Contains(t, lines[0], `"content":"hel"`)
	assert.Contains(t, lines[1], "upstream_truncated")
	assert.Equal(t, modelgateway.OutcomeTruncated, cut.lastUsage(t).Outcome)

	closed, _ := codexBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, event(`{"type":"response.created","response":{}}`))
	})
	empty := newCodexGateway(t, closed, &accounts{})

	status, _, body = empty.call(t, empty.key, askSol+`}`)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, body, "upstream_truncated")
}

// Scenario: no Codex account is logged in. The provider cannot answer, so
// the model's other target does; with none, the client is told no provider
// could be reached, and the backend is never called.
func TestWithNoAccountTheProviderIsSkipped(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(helloEvents...))
	g := newCodexGateway(t, backend, &accounts{none: true})

	status, _, body := g.call(t, g.key, askSol+`}`)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, body, "upstream_unavailable")
	assert.Zero(t, got.calls())

	second, _ := provider(t, plain(http.StatusOK, answer))
	withFallback := newCodexGateway(t, backend, &accounts{none: true}, second)

	status, _, _ = withFallback.call(t, withFallback.key, askSol+`}`)
	assert.Equal(t, http.StatusOK, status)
}

// Scenario: two Codex accounts are logged in and the first is rate limited.
// The same request is served by the second, and the client sees only the
// answer. The first cools down two seconds; limited again it cools four; once
// it has answered, the count starts over. With both limited the client gets
// the last refusal.
func TestTheNextAccountServesWhenOneIsLimited(t *testing.T) {
	t.Parallel()

	limited := map[string]bool{"Bearer access-0": true}

	var mu sync.Mutex

	backend, got := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refuse := limited[r.Header.Get("Authorization")]
		mu.Unlock()

		if refuse {
			plain(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`)(w, r)

			return
		}

		stream(helloEvents...)(w, r)
	})
	subs := &accounts{ids: []string{"acc_1", "acc_2"}, keepCooling: true}
	g := newCodexGateway(t, backend, subs)

	status, _, body := g.call(t, g.key, askSol+`}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 2, got.calls())

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer acc_2-access-0", sentHeaders.Get("Authorization"))
	assert.Equal(t, []time.Duration{2 * time.Second}, subs.waited("acc_1"))
	assert.Empty(t, subs.waited("acc_2"))

	status, _, _ = g.call(t, g.key, askSol+`}`)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second}, subs.waited("acc_1"))

	mu.Lock()
	limited = map[string]bool{}
	mu.Unlock()

	status, _, _ = g.call(t, g.key, askSol+`}`)
	require.Equal(t, http.StatusOK, status)

	mu.Lock()
	limited = map[string]bool{"Bearer access-0": true, "Bearer acc_2-access-0": true}
	mu.Unlock()

	status, _, body = g.call(t, g.key, askSol+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, body, "slow down")
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 2 * time.Second}, subs.waited("acc_1"),
		"an answer in between starts the count over")
	assert.Equal(t, []time.Duration{2 * time.Second}, subs.waited("acc_2"))
}

// Scenario: what each kind of refusal costs an account. A payment or
// permission refusal cools it two minutes and the next account is tried; a
// server error that is no overload cools it thirty seconds and reaches the
// client, since another account would not help; a request the backend finds
// wrong costs nothing.
func TestARefusalCoolsTheAccountByItsKind(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		status int
		wait   []time.Duration
		calls  int
	}{
		"forbidden":      {http.StatusForbidden, []time.Duration{2 * time.Minute}, 2},
		"server error":   {http.StatusInternalServerError, []time.Duration{30 * time.Second}, 1},
		"bad request":    {http.StatusBadRequest, nil, 1},
		"overloaded 503": {http.StatusServiceUnavailable, []time.Duration{2 * time.Second}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend, got := codexBackend(t, plain(c.status, `{"error":{"message":"no"}}`))
			subs := &accounts{ids: []string{"acc_1", "acc_2"}}
			g := newCodexGateway(t, backend, subs)

			status, _, _ := g.call(t, g.key, askSol+`}`)
			assert.Equal(t, c.status, status)
			assert.Equal(t, c.wait, subs.waited("acc_1"))
			assert.Equal(t, c.calls, got.calls())
		})
	}
}

// Scenario: the Codex backend says on every answer what percentage of the
// account's primary and secondary windows is used. The
// gateway passes on what is left under the fuller of the two.
func TestACodexAnswerSaysWhatIsLeftOfTheAccount(t *testing.T) {
	t.Parallel()

	backend, _ := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Codex-Primary-Used-Percent", "20")
		w.Header().Set("X-Codex-Secondary-Used-Percent", "95.5")
		stream(helloEvents...)(w, r)
	})

	acc := &accounts{}
	g := newCodexGateway(t, backend, acc)

	status, _, body := g.call(t, g.key, askSol+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)

	left, said := acc.quotaNoted("acc_1")
	require.True(t, said)
	assert.InDelta(t, 0.045, left, 1e-9, "one minus the fuller window")
}
