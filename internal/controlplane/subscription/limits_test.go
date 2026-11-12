package subscription_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// Scenario: the owner asks how much of a Claude subscription is used. The call
// is Claude Code's own, with the account's token; the answer's two windows and
// a model's own week come back with their reset times. A minute later nothing
// is asked again unless the owner asks for a fresh reading.
func TestAClaudeAccountSaysItsLimits(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	var calls atomic.Int32

	w.oauth.answerUsage(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "Bearer sk-ant-oat-1", r.Header.Get("Authorization"))
		assert.Equal(t, "oauth-2025-04-20", r.Header.Get("Anthropic-Beta"))

		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{
			"five_hour": {"utilization": 37.5, "resets_at": "2026-11-12T15:00:00Z"},
			"seven_day": {"utilization": 120, "resets_at": 1794787200},
			"seven_day_opus": {"utilization": 8, "resets_at": null},
			"seven_day_sonnet": null,
			"limits": [{"kind": "weekly_scoped", "percent": 9, "scope": {"model": {"display_name": "Opus"}}},
				{"kind": "weekly_scoped", "percent": 4, "scope": {"model": {"display_name": "Fable"}}}]
		}`))
	})

	limits, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, []subscription.Window{
		{Name: "5 hours", UsedPercent: 37.5, ResetsAt: time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)},
		{Name: "week", UsedPercent: 100, ResetsAt: time.Unix(1794787200, 0).UTC()},
		{Name: "week (Opus)", UsedPercent: 8},
		{Name: "week (Fable)", UsedPercent: 4},
	}, limits.Windows)

	w.clock.advance(30 * time.Second)

	_, err = w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load(), "a reading under a minute old is given again")

	_, err = w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load(), "a fresh reading asks the vendor")
}

// Scenario: Anthropic answers 429 to the usage call. The last reading is
// still given, and the vendor is left alone for the wait it named, even when
// the owner asks for a fresh reading; after it the vendor is asked again.
func TestAClaudeAccountsLimitsRestAfterA429(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	var calls atomic.Int32

	var refuse atomic.Bool

	w.oauth.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)

		if refuse.Load() {
			rw.Header().Set("Retry-After", "120")
			rw.WriteHeader(http.StatusTooManyRequests)

			return
		}

		_, _ = rw.Write([]byte(`{"five_hour": {"utilization": 10}}`))
	})

	first, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)

	refuse.Store(true)

	w.clock.advance(2 * time.Minute)

	again, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, first, again, "the last reading stands in for the refused one")
	assert.EqualValues(t, 2, calls.Load())

	w.clock.advance(time.Minute)

	_, err = w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load(), "the vendor is not asked during the wait it named")

	refuse.Store(false)

	w.clock.advance(2 * time.Minute)

	_, err = w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.EqualValues(t, 3, calls.Load())
}

// Scenario: an account whose token the vendor refuses has no reading to
// give: the owner is told to sign it in again.
func TestARefusedTokenSaysToSignInAgain(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	w.oauth.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusForbidden)
	})

	_, err = w.subs.Limits(t.Context(), a.ID, false)
	require.ErrorContains(t, err, "sign the account in again")
}

// Scenario: the owner asks how much of a Codex subscription is used. ChatGPT
// is asked with the account's token and its ChatGPT account id; the window of
// five hours and the one of a week are told apart by their length.
func TestACodexAccountSaysItsLimits(t *testing.T) {
	w := newWorld(t)
	a := w.login()

	chatgpt := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/backend-api/wham/usage", r.URL.Path)
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		assert.Equal(t, a.ExternalID, r.Header.Get("Chatgpt-Account-Id"))

		_, _ = rw.Write([]byte(`{"plan_type": "plus", "rate_limit": {
			"primary_window": {"used_percent": 12, "limit_window_seconds": 18000, "reset_at": 1794787200},
			"secondary_window": {"used_percent": 61.2, "limit_window_seconds": 604800, "reset_at": 1795000000}
		}}`))
	}))
	t.Cleanup(chatgpt.Close)
	w.codex.ReadLimitsFrom(chatgpt.URL)

	limits, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, []subscription.Window{
		{Name: "5 hours", UsedPercent: 12, ResetsAt: time.Unix(1794787200, 0).UTC()},
		{Name: "week", UsedPercent: 61.2, ResetsAt: time.Unix(1795000000, 0).UTC()},
	}, limits.Windows)
}
