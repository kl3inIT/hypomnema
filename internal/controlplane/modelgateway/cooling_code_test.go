package modelgateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Why an account rests, in one closed set for every adapter (issue #1305).

// Scenario: the only OpenCode Go key reaches Go's 5-hour, weekly or monthly limit. Go answers 429 with the limit and
// when it resets in its words ("Resets in 2hr 15min", omp packages/utils/test/fetch-retry.test.ts:197-206). The key
// rests as a usage limit, not a refused key, until the reset the words sum to, and keeps Go's words.
func TestAnOpenCodeGoWindowUsedUpIsAUsageLimitUntilItsReset(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		message string
		reset   time.Duration
	}{
		"5-hour": {"5-hour usage limit reached. Resets in 2hr 15min. To continue using this model now, enable " +
			"usage from your available balance: https://opencode.ai/workspace/wrk_1/go", 2*time.Hour + 15*time.Minute},
		"weekly":           {"Weekly usage limit reached. Resets in 3 days.", 3 * 24 * time.Hour},
		"monthly":          {"Monthly usage limit reached. Resets in 1 day.", 24 * time.Hour},
		"5-hour, minutes":  {"5-hour usage limit reached. Resets in 45min.", 45 * time.Minute},
		"weekly, compound": {"Weekly usage limit reached. Resets in 2 days 3hr.", 51 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body, err := json.Marshal(map[string]any{"error": map[string]string{"message": c.message}})
			require.NoError(t, err)

			backend := newGoBackend(t, plain(http.StatusTooManyRequests, string(body)))
			subs := &accounts{}
			g := newGoGateway(t, backend, subs)

			status, _, answer := g.call(t, g.key, fmt.Sprintf(askGo, "glm-5.3"))
			require.Equal(t, http.StatusTooManyRequests, status, answer)

			assert.Equal(t, modelgateway.CoolingUsageLimit, subs.codeOf("acc_1"))
			assert.Equal(t, c.message, subs.lastReason(), "Go's own words, beside the code")
			assert.WithinDuration(t, time.Now().Add(c.reset), subs.cooledUntil(), time.Minute,
				"the key rests until the reset Go's words sum to")
		})
	}
}

// adapterCase is a built-in provider's gateway and the body its backend refuses with.
type adapterCase struct {
	name string
	// serve starts the provider's backend answering every request with handle and returns a gateway over it, the
	// request to send it, and why its only account rests.
	serve func(t *testing.T, handle http.HandlerFunc) (g chatGateway, ask string, rests func() modelgateway.CoolingCode)
	body  func(status int) string
}

func openAIBody(status int) string {
	return fmt.Sprintf(`{"error":{"message":"refused with %d"}}`, status)
}

var adapterCases = []adapterCase{
	{
		name: "opencode go",
		serve: func(t *testing.T, handle http.HandlerFunc) (chatGateway, string, func() modelgateway.CoolingCode) {
			t.Helper()

			subs := &accounts{}

			return newGoGateway(t, newGoBackend(t, handle), subs), fmt.Sprintf(askGo, "glm-5.3"),
				func() modelgateway.CoolingCode { return subs.codeOf("acc_1") }
		},
		body: openAIBody,
	},
	{
		name: "opencode zen",
		serve: func(t *testing.T, handle http.HandlerFunc) (chatGateway, string, func() modelgateway.CoolingCode) {
			t.Helper()

			subs := newByRuntime("acc_1")

			return newZenGateway(t, newGoBackend(t, handle), newGoBackend(t, chatHi), subs),
				fmt.Sprintf(askZen, "glm-5.3"),
				func() modelgateway.CoolingCode { return subs.of(orchestration.OpenCodeZenRuntime).codeOf("acc_1") }
		},
		body: openAIBody,
	},
	{
		name: "claude",
		serve: func(t *testing.T, handle http.HandlerFunc) (chatGateway, string, func() modelgateway.CoolingCode) {
			t.Helper()

			subs := &accounts{}
			backend, _ := claudeBackend(t, handle)

			return newClaudeGateway(t, backend, subs), askClaude + `,"stream":true}`,
				func() modelgateway.CoolingCode { return subs.codeOf("acc_1") }
		},
		body: func(status int) string {
			return fmt.Sprintf(`{"type":"error","error":{"type":"error","message":"refused with %d"}}`, status)
		},
	},
	{
		name: "codex",
		serve: func(t *testing.T, handle http.HandlerFunc) (chatGateway, string, func() modelgateway.CoolingCode) {
			t.Helper()

			subs := &accounts{}
			backend, _ := codexBackend(t, handle)

			return newCodexGateway(t, backend, subs), askSol + `}`,
				func() modelgateway.CoolingCode { return subs.codeOf("acc_1") }
		},
		body: openAIBody,
	},
	{
		name: "antigravity",
		serve: func(t *testing.T, handle http.HandlerFunc) (chatGateway, string, func() modelgateway.CoolingCode) {
			t.Helper()

			subs := &googleAccount{}
			backend, _ := cloudCode(t, handle)

			return newAntigravityGateway(t, backend, subs), askFlash + `}`,
				func() modelgateway.CoolingCode { return subs.codeOf("acc_1") }
		},
		body: func(status int) string {
			return fmt.Sprintf(`{"error":{"code":%d,"message":"refused with %d"}}`, status, status)
		},
	},
}

// Scenario: each built-in provider refuses its only account with a 401, a 402, a plain 429 or a 503. Every adapter
// reads the refusal the one way (issue #1305, row 2): a refused key, credits spent, a rate limit, an overloaded
// provider. The account that rests keeps that code; the gateway's 429 once no account can serve says it too (row 7).
// Antigravity rests one model of the account after a 429 (antigravityRefused), so its code shows in that 429 only.
func TestEveryAdapterNamesWhyItsAccountRests(t *testing.T) {
	t.Parallel()

	statuses := map[int]modelgateway.CoolingCode{
		http.StatusUnauthorized:       modelgateway.CoolingAuth,
		http.StatusPaymentRequired:    modelgateway.CoolingBilling,
		http.StatusTooManyRequests:    modelgateway.CoolingRateLimit,
		http.StatusServiceUnavailable: modelgateway.CoolingOverloaded,
	}

	for _, c := range adapterCases {
		for status, code := range statuses {
			t.Run(fmt.Sprintf("%s %d", c.name, status), func(t *testing.T) {
				t.Parallel()

				g, ask, rests := c.serve(t, plain(status, c.body(status)))

				first, _, body := g.call(t, g.key, ask)
				require.Equal(t, status, first, body)

				got := rests()

				if c.name == "antigravity" && status == http.StatusTooManyRequests {
					assert.Empty(t, got, "the model rests, not the account")
				} else {
					assert.Equal(t, code, got, "why the account rests")
				}

				// The account rests now: the next request gets the gateway's own 429, which says why.
				again, headers, body := g.call(t, g.key, ask)
				require.Equal(t, http.StatusTooManyRequests, again, body)
				assert.NotEmpty(t, headers.Get("Retry-After"))

				var refusal struct {
					Error struct {
						Code        string `json:"code"`
						CoolingCode string `json:"cooling_code"`
					} `json:"error"`
				}

				require.NoError(t, json.Unmarshal([]byte(body), &refusal), body)
				assert.Equal(t, "account_unavailable", refusal.Error.Code)
				assert.Equal(t, string(code), refusal.Error.CoolingCode, body)
			})
		}
	}
}

// Scenario: Claude's 429 is its 5-hour or weekly window used up only when Anthropic says the window is rejected
// (anthropic-ratelimit-unified-status, OmniRoute open-sse/services/claudeLowPriority.ts:241-246); without it, a 429 is
// a rate limit. Either rests the account as long as before (row 8).
func TestAClaude429IsAUsageLimitWhenItsWindowIsRejected(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		unified string
		code    modelgateway.CoolingCode
	}{
		"window rejected": {"rejected", modelgateway.CoolingUsageLimit},
		"a throttle":      {"", modelgateway.CoolingRateLimit},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend, _ := claudeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				if c.unified != "" {
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", c.unified)
				}

				plain(http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error",`+
					`"message":"This request would exceed your account's rate limit. Please try again later."}}`)(w, r)
			})

			acc := &accounts{}
			g := newClaudeGateway(t, backend, acc)

			status, _, body := g.call(t, g.key, askClaude+`,"stream":true}`)
			require.Equal(t, http.StatusTooManyRequests, status, body)
			assert.Equal(t, c.code, acc.lastCode())
			assert.Equal(t, "This request would exceed your account's rate limit. Please try again later.",
				acc.lastReason())
			assert.WithinDuration(t, time.Now().Add(time.Hour), acc.cooledUntil(), time.Minute,
				"a Claude 429 rests the account an hour when Anthropic names no reset, as before")
		})
	}
}

// Scenario: the gateway's 429 when every account rests keeps its shape on each route (account_unavailable,
// resets_at, Retry-After) and says why the first account free again rests, in a field and in its words, so a
// client's log says "usage limit" (LiteLLM types/router.py:1273-1303 names its cause in its type and message).
func TestTheNoAccount429SaysWhyTheAccountRests(t *testing.T) {
	t.Parallel()

	backend := newGoBackend(t, plain(http.StatusTooManyRequests,
		`{"error":{"message":"5-hour usage limit reached. Resets in 2hr 15min."}}`))
	subs := &accounts{}
	g := newGoGateway(t, backend, subs)

	status, _, body := g.call(t, g.key, fmt.Sprintf(askGo, "glm-5.3"))
	require.Equal(t, http.StatusTooManyRequests, status, body)
	require.Equal(t, modelgateway.CoolingUsageLimit, subs.codeOf("acc_1"))

	for route, send := range map[string]func() (int, http.Header, string){
		"/v1/chat/completions": func() (int, http.Header, string) {
			return g.call(t, g.key, fmt.Sprintf(askGo, "glm-5.3"))
		},
		"/v1/messages": func() (int, http.Header, string) {
			return g.send(t, "/v1/messages",
				`{"model":"opencode-go/minimax-m3","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
				claudeCodeHeaders(g.key)...)
		},
	} {
		status, headers, body := send()
		require.Equal(t, http.StatusTooManyRequests, status, route+": "+body)

		retry := headers.Get("Retry-After")
		assert.NotEmpty(t, retry, route)

		var refusal struct {
			Error struct {
				Message     string `json:"message"`
				Code        string `json:"code"`
				ResetsAt    int64  `json:"resets_at"`
				CoolingCode string `json:"cooling_code"`
			} `json:"error"`
		}

		require.NoError(t, json.Unmarshal([]byte(body), &refusal), body)
		assert.Equal(t, "account_unavailable", refusal.Error.Code, route)
		assert.Equal(t, "usage_limit", refusal.Error.CoolingCode, route)
		assert.Equal(t, "no account of this provider can be used for now (usage limit)", refusal.Error.Message, route)
		assert.InDelta(t, time.Now().Add(2*time.Hour+15*time.Minute).Unix(), refusal.Error.ResetsAt, 60, route)
	}
}

// Scenario: when a refused account may serve again, as omp reads it (packages/utils/src/fetch-retry.ts:80-117 and
// 13-22): the headers first, retry-after-ms, Retry-After as seconds or a date, x-ratelimit-reset-ms,
// x-ratelimit-reset, x-ratelimit-reset-after; else the provider's words, each number and unit after "resets in"
// summed; nothing when neither says.
func TestAResetIsReadFromTheHeadersThenTheWords(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	words := []byte("5-hour usage limit reached. Resets in 2hr 15min.")

	for name, c := range map[string]struct {
		headers map[string]string
		body    []byte
		want    time.Duration
	}{
		"2hr 15min":            {nil, words, 2*time.Hour + 15*time.Minute},
		"3 days":               {nil, []byte("Weekly usage limit reached. Resets in 3 days."), 72 * time.Hour},
		"1 day":                {nil, []byte("Monthly usage limit reached. Resets in 1 day."), 24 * time.Hour},
		"71h48m47s":            {nil, []byte("Individual quota reached. Resets in 71h48m47s."), 71*time.Hour + 48*time.Minute + 47*time.Second},
		"2 hours and 5 min":    {nil, []byte("Your limit will reset in 2 hours and 5 minutes"), 2*time.Hour + 5*time.Minute},
		"30 sec":               {nil, []byte("resets in 30 sec"), 30 * time.Second},
		"Retry-After first":    {map[string]string{"Retry-After": "120"}, words, 2 * time.Minute},
		"retry-after-ms first": {map[string]string{"Retry-After-Ms": "1500", "Retry-After": "120"}, words, 1500 * time.Millisecond},
		"Retry-After date": {
			map[string]string{"Retry-After": now.Add(10 * time.Minute).Format(http.TimeFormat)}, nil, 10 * time.Minute,
		},
		"x-ratelimit-reset-ms delta": {map[string]string{"X-Ratelimit-Reset-Ms": "90000"}, nil, 90 * time.Second},
		"x-ratelimit-reset epoch": {
			map[string]string{"X-Ratelimit-Reset": fmt.Sprint(now.Add(time.Hour).Unix())}, nil, time.Hour,
		},
		"x-ratelimit-reset-after": {map[string]string{"X-Ratelimit-Reset-After": "45"}, nil, 45 * time.Second},
		"nothing":                 {nil, []byte(`{"error":{"message":"Too many requests"}}`), 0},
		"not a unit":              {nil, []byte("Resets in 5 parsecs"), 0},
	} {
		h := http.Header{}
		for k, v := range c.headers {
			h.Set(k, v)
		}

		got := modelgateway.ProviderReset(h, c.body, now)
		if c.want == 0 {
			assert.True(t, got.IsZero(), "%s: %v", name, got)

			continue
		}

		assert.Equal(t, now.Add(c.want), got.UTC(), name)
	}
}
