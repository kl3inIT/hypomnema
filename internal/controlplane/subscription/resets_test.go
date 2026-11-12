package subscription_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// Scenario: a Claude account has limit resets (issue #199). The limits call,
// asked the way Claude Code asks, names them: the ones that can be used are
// counted and the one Anthropic says is next is the one used. Using it claims
// that grant in the account's organization with the caller's request id, and
// the limits are read again. Anthropic's "not limited" and "cooldown" are told
// apart from a reset that was done.
func TestAClaudeAccountUsesALimitReset(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	var mu sync.Mutex

	used, readings := 62.0, 0

	w.oauth.answerUsage(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "1", r.URL.Query().Get("cedar_ember"), "the resets are named only when asked for")

		mu.Lock()
		defer mu.Unlock()

		readings++

		writeJSON(rw, map[string]any{
			"five_hour": map[string]any{"utilization": used, "resets_at": "2026-11-12T15:00:00Z"},
			"cedar_ember": map[string]any{
				"eligible": true, "next_grant_id": "grant_b",
				"grants": []map[string]any{
					{"id": "grant_a", "resets_left": 1, "ends_at": "2026-12-01T00:00:00Z", "usable_now": true},
					{"id": "grant_b", "resets_left": 2, "ends_at": "2026-11-20T00:00:00Z", "usable_now": true},
					{"id": "paused", "resets_left": 5, "paused": true},
					{"id": "ended", "resets_left": 5, "ends_at": "2020-01-01T00:00:00Z"},
					{"id": "spent", "resets_left": 0},
				},
			},
		})
	})

	limits, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, 3, limits.Resets.Available, "a paused, an ended and a spent grant are not counted")
	assert.Equal(t, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), limits.Resets.ExpiresAt)

	var claims []map[string]string

	result := "reset"

	w.oauth.answerReset(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "org-uuid-1", r.PathValue("org"))
		assert.Equal(t, "oauth-2025-04-20", r.Header.Get("Anthropic-Beta"))

		var claim map[string]string
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&claim))

		mu.Lock()
		defer mu.Unlock()

		claims = append(claims, claim)
		if result == "reset" {
			used = 0
		}

		writeJSON(rw, map[string]string{"result": result})
	})

	_, err = w.subs.UseReset(t.Context(), a.ID, "not a request id!")
	require.ErrorIs(t, err, subscription.ErrBadResetRequest)
	assert.Empty(t, claims, "nothing is sent for a request the vendor would not take")

	before := readings

	after, err := w.subs.UseReset(t.Context(), a.ID, "req-1")
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.Equal(t, map[string]string{"program": "cedar_ember", "grant_id": "grant_b", "request_id": "req-1"}, claims[0])
	require.Len(t, after.Windows, 1)
	assert.Zero(t, after.Windows[0].UsedPercent, "the limits are read again after the reset")
	assert.Equal(t, before+2, readings, "once to choose the reset, once to see what it did")

	mu.Lock()
	result = "not_limited"
	mu.Unlock()

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-2")
	require.ErrorIs(t, err, subscription.ErrNothingToReset)

	mu.Lock()
	result = "cooldown"
	mu.Unlock()

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-3")
	require.ErrorIs(t, err, subscription.ErrResetWait)

	mu.Lock()
	result = "already_used"
	mu.Unlock()

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-1")
	require.NoError(t, err, "a request sent again is the reset it already was")
}

// Scenario: a Claude account that Anthropic gives no resets shows none, and
// none can be used.
func TestAClaudeAccountWithoutResetsHasNoneToUse(t *testing.T) {
	w := newClaudeWorld(t)
	a, err := w.login()
	require.NoError(t, err)

	w.oauth.answerUsage(func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, map[string]any{
			"five_hour":   map[string]any{"utilization": 10},
			"cedar_ember": map[string]any{"eligible": false, "grants": []map[string]any{{"id": "g", "resets_left": 3}}},
		})
	})
	w.oauth.answerReset(func(http.ResponseWriter, *http.Request) {
		t.Error("no reset is claimed for an account that has none")
	})

	limits, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Zero(t, limits.Resets.Available)

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-1")
	require.ErrorIs(t, err, subscription.ErrNoResets)
}

// Scenario: a Codex account has reset credits. ChatGPT lists them; a credit
// that is redeemed or has ended is left out and the one that ends soonest is
// used. Using it sends that credit and the caller's request id; "already
// redeemed" is the reset done, and "nothing to reset" is told apart.
func TestACodexAccountUsesALimitReset(t *testing.T) {
	w := newWorld(t)
	a := w.login()

	var mu sync.Mutex

	var consumed []map[string]string

	outcome := "reset"

	chatgpt := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, a.ExternalID, r.Header.Get("Chatgpt-Account-Id"))

		switch r.URL.Path {
		case "/backend-api/wham/usage":
			_, _ = rw.Write([]byte(`{"rate_limit": {"primary_window": {"used_percent": 100, "limit_window_seconds": 18000}}}`))
		case "/backend-api/wham/rate-limit-reset-credits":
			_, _ = rw.Write([]byte(`{"credits": [
				{"credit_id": "late", "status": "available", "expires_at": "2027-03-01T00:00:00Z"},
				{"credit_id": "soon", "status": "available", "expires_at": "2027-01-01T00:00:00Z"},
				{"credit_id": "gone", "status": "redeemed", "expires_at": "2027-01-01T00:00:00Z"},
				{"credit_id": "past", "status": "available", "expires_at": "2020-01-01T00:00:00Z"},
				{"creditId": "open", "available": true}
			]}`))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			var body map[string]string
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))

			mu.Lock()
			consumed = append(consumed, body)
			answer := outcome
			mu.Unlock()

			if answer == "nothing_to_reset" {
				rw.WriteHeader(http.StatusConflict)
			}

			_, _ = rw.Write([]byte(`{"outcome": "` + answer + `"}`))
		default:
			t.Errorf("an unexpected call: %s", r.URL.Path)
		}
	}))
	t.Cleanup(chatgpt.Close)
	w.codex.ReadLimitsFrom(chatgpt.URL)

	limits, err := w.subs.Limits(t.Context(), a.ID, false)
	require.NoError(t, err)
	assert.Equal(t, 3, limits.Resets.Available, "a redeemed credit and one that ended are left out")
	assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), limits.Resets.ExpiresAt, "the soonest to end is next")

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-1")
	require.NoError(t, err)
	require.Len(t, consumed, 1)
	assert.Equal(t, map[string]string{"redeem_request_id": "req-1", "credit_id": "soon"}, consumed[0])

	mu.Lock()
	outcome = "alreadyRedeemed"
	mu.Unlock()

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-1")
	require.NoError(t, err, "a credit already redeemed is the reset done")

	mu.Lock()
	outcome = "nothing_to_reset"
	mu.Unlock()

	_, err = w.subs.UseReset(t.Context(), a.ID, "req-2")
	require.ErrorIs(t, err, subscription.ErrNothingToReset)
}
