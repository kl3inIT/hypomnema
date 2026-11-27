package subscription_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
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
	assert.Equal(t, first.Windows, again.Windows, "the last reading stands in for the refused one")
	assert.Equal(t, first.ReadAt, again.ReadAt)
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

// Scenario (issue #1198): an account read once stops being read: Anthropic
// answers 429, then 401. Each time the last reading is still given, with why
// it is not fresh: resting with when it is asked again, then refused. A new
// token is asked again, and a reading that succeeds clears the reason. Each
// failed call is logged when its reason changes, not when the reason is given
// again without a call.
func TestAnEarlierReadingSaysWhyItIsNotFresh(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	var (
		calls  atomic.Int32
		answer atomic.Int32 // the status Anthropic answers; 0 is 200
	)

	w.oauth.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)

		switch status := int(answer.Load()); status {
		case 0:
			_, _ = rw.Write([]byte(`{"five_hour": {"utilization": ` + strconv.Itoa(int(calls.Load())*10) + `}}`))
		case http.StatusTooManyRequests:
			rw.Header().Set("Retry-After", "120")
			rw.WriteHeader(status)
		default:
			rw.WriteHeader(status)
			_, _ = rw.Write([]byte(`{"error": {"message": "OAuth token has been revoked"}}`))
		}
	})

	first, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	require.NoError(t, first.Stale, "a reading just made is fresh")

	failures := func() []map[string]any { return w.logs.events("subscription.limits_failed") }

	// A 429: the last reading, resting until the wait Anthropic named.
	answer.Store(http.StatusTooManyRequests)
	w.clock.advance(2 * time.Minute)

	resting, err := w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	assert.Equal(t, first.Windows, resting.Windows)
	assert.Equal(t, first.ReadAt, resting.ReadAt)

	var rest *subscription.LimitsRestingError
	require.ErrorAs(t, resting.Stale, &rest)
	assert.Equal(t, w.clock.Now().Add(2*time.Minute), rest.Until)
	require.Len(t, failures(), 1)
	assert.Equal(t, "resting", failures()[0]["reason"])
	assert.EqualValues(t, http.StatusTooManyRequests, failures()[0]["status"])
	assert.Equal(t, a.ID, failures()[0]["account_id"])

	w.clock.advance(time.Minute)

	again, err := w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	require.ErrorAs(t, again.Stale, &rest, "the rest is still said while the vendor is not asked")
	assert.EqualValues(t, 2, calls.Load())
	assert.Len(t, failures(), 1, "a reason given again without a call is not logged")

	// A 401 after the rest: the last reading, refused, in the vendor's words.
	answer.Store(http.StatusUnauthorized)
	w.clock.advance(2 * time.Minute)

	refused, err := w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	assert.Equal(t, first.Windows, refused.Windows)
	require.ErrorIs(t, refused.Stale, subscription.ErrLimitsRefused)
	require.ErrorContains(t, refused.Stale, "revoked")
	require.Len(t, failures(), 2)
	assert.Equal(t, "refused", failures()[1]["reason"])
	assert.EqualValues(t, http.StatusUnauthorized, failures()[1]["status"])

	_, err = w.subs.Limits(t.Context(), a.ID, true)
	require.NoError(t, err)
	assert.EqualValues(t, 3, calls.Load(), "the refused token is not sent again")
	assert.Len(t, failures(), 2)

	// The token is refreshed: the new one is asked, and its reading clears the reason.
	answer.Store(0)
	w.clock.advance(3 * time.Hour)

	fresh, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.EqualValues(t, 4, calls.Load(), "a new token is asked again")
	require.NoError(t, fresh.Stale)
	assert.Equal(t, w.clock.Now(), fresh.ReadAt)
	assert.NotEqual(t, first.Windows, fresh.Windows)

	// A failure after a success is logged again, once while it stays the same.
	answer.Store(http.StatusInternalServerError)

	for range 2 {
		w.clock.advance(2 * time.Minute)

		failed, err := w.subs.Limits(t.Context(), a.ID, false)
		require.NoError(t, err)
		assert.Equal(t, fresh.Windows, failed.Windows)
		require.ErrorContains(t, failed.Stale, "500")
		require.NotErrorIs(t, failed.Stale, subscription.ErrLimitsRefused)
	}

	assert.EqualValues(t, 6, calls.Load(), "a call that failed otherwise is made again")
	require.Len(t, failures(), 3)
	assert.Equal(t, "failed", failures()[2]["reason"])
}

// Scenario: a Claude account added with a pasted token serves models but
// Anthropic refuses its token for the limits call. The owner is told why, in
// Anthropic's own words, and the vendor is not asked again with that token.
// Once the account has served an answer through the gateway, its limits are
// what that answer's headers said; a window whose reset has passed is left
// out.
func TestARefusedTokenGivesWhatItsAnswersSaid(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	var calls atomic.Int32

	w.oauth.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte(`{"error": {"message": "OAuth token does not meet scope requirement user:profile"}}`))
	})

	_, err = w.subs.Limits(t.Context(), a.ID, false)
	require.ErrorContains(t, err, "user:profile")
	require.ErrorContains(t, err, "accounts login")

	_, err = w.subs.Limits(t.Context(), a.ID, true)
	require.ErrorContains(t, err, "user:profile")
	assert.EqualValues(t, 1, calls.Load(), "a token the vendor refused is not sent again")

	fiveHours := w.clock.Now().Add(2 * time.Hour)
	week := w.clock.Now().Add(72 * time.Hour)

	answer := http.Header{}
	answer.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	answer.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(fiveHours.Unix(), 10))
	answer.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.93")
	answer.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(week.Unix(), 10))
	w.subs.NoteAnswer("claude", a.ID, answer)
	// An answer that names no window changes nothing.
	w.subs.NoteAnswer("claude", a.ID, http.Header{})

	heardAt := w.clock.Now()

	limits, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, heardAt, limits.ReadAt)
	require.Len(t, limits.Windows, 2)
	assert.Equal(t, "5 hours", limits.Windows[0].Name)
	assert.InDelta(t, 42, limits.Windows[0].UsedPercent, 1e-9)
	assert.Equal(t, fiveHours.Unix(), limits.Windows[0].ResetsAt.Unix())
	assert.Equal(t, "week", limits.Windows[1].Name)
	assert.InDelta(t, 93, limits.Windows[1].UsedPercent, 1e-9)

	w.clock.advance(3 * time.Hour)

	limits, err = w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	require.Len(t, limits.Windows, 1, "the 5 hours started again: what it holds is not known")
	require.ErrorIs(t, limits.Stale, subscription.ErrLimitsRefused, "what was heard says the call is refused")
	assert.Equal(t, "week", limits.Windows[0].Name)
	assert.EqualValues(t, 2, calls.Load(), "the token was refreshed meanwhile: a new token is tried once")
}

// Scenario: the owner asks how much of a Codex subscription is used. ChatGPT
// is asked with the account's token and its ChatGPT account id; the window of
// five hours and the one of a week are told apart by their length.
func TestACodexAccountSaysItsLimits(t *testing.T) {
	w := newWorld(t)
	a := w.login()

	chatgpt := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// The account's limit resets are read after its windows; this account has none.
		if r.URL.Path == "/backend-api/wham/rate-limit-reset-credits" {
			_, _ = rw.Write([]byte(`[]`))

			return
		}

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
