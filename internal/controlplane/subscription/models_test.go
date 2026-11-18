package subscription_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #623): Codex serves a model the provider's fixed list lacks. The control plane asks the Codex
// backend with the account's token and ChatGPT account, as the Codex CLI does, and is told the models Codex lists in
// its picker and serves to the API; one it hides and one it does not serve to the API are left out.
func TestACodexAccountSaysWhichModelsItIsServed(t *testing.T) {
	w := newWorld(t)
	a := w.login()

	chatgpt := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/backend-api/codex/models", r.URL.Path)
		// The version of Codex's latest release, rust-v0.169.1, as its CLI names its own (issue #795).
		assert.Equal(t, "0.169.1", r.URL.Query().Get("client_version"))
		assert.Equal(t, "0.169.1", r.Header.Get("Version"))
		assert.Equal(t, "codex_cli_rs", r.Header.Get("Originator"))
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		assert.Equal(t, a.ExternalID, r.Header.Get("Chatgpt-Account-Id"))

		_, _ = rw.Write([]byte(`{"models": [
			{"slug": "gpt-6.2-sol", "visibility": "list", "supported_in_api": true},
			{"slug": "gpt-6.2-astra", "visibility": "list", "supported_in_api": true},
			{"slug": "gpt-6.1-terra", "visibility": "list", "supported_in_api": true},
			{"slug": "gpt-6.1-sol", "visibility": "list", "supported_in_api": true},
			{"slug": "codex-auto-review", "visibility": "hide", "supported_in_api": true},
			{"slug": "gpt-daybreak-blue-latest", "visibility": "list", "supported_in_api": false}
		]}`))
	}))
	t.Cleanup(chatgpt.Close)
	w.codex.ReadLimitsFrom(chatgpt.URL)

	names, ok := w.subs.ServedNames(t.Context(), "codex")
	require.True(t, ok)
	assert.Equal(t, []string{"gpt-6.1-sol", "gpt-6.1-terra", "gpt-6.2-astra", "gpt-6.2-sol"}, names)
}

// Scenario (issue #623): Anthropic lists the models a Claude subscription is served over two pages. The control
// plane reads both with the OAuth token and the plain OAuth beta. An hour's reading is given again without asking;
// after an hour Anthropic is asked again.
func TestAClaudeAccountSaysWhichModelsItIsServed(t *testing.T) {
	w := newClaudeWorld(t)
	_, err := w.login()
	require.NoError(t, err)

	var calls atomic.Int32

	w.oauth.answerModels(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The account's token; it is made fresh as it nears its end.
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer sk-ant-oat-")
		assert.Equal(t, "oauth-2025-04-20", r.Header.Get("Anthropic-Beta"))
		assert.NotEmpty(t, r.Header.Get("Anthropic-Version"))

		if r.URL.Query().Get("after_id") == "" {
			_, _ = rw.Write([]byte(`{"data": [{"id": "claude-opus-5-5"}, {"id": "claude-sonnet-5-5"}],
				"has_more": true, "last_id": "claude-sonnet-5-5"}`))

			return
		}

		assert.Equal(t, "claude-sonnet-5-5", r.URL.Query().Get("after_id"))
		_, _ = rw.Write([]byte(`{"data": [{"id": "claude-haiku-5-5"}], "has_more": false}`))
	})

	names, ok := w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.Equal(t, []string{"claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5"}, names)
	assert.EqualValues(t, 2, calls.Load())

	w.clock.advance(30 * time.Minute)

	_, ok = w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.EqualValues(t, 2, calls.Load(), "a reading under an hour old is given again")

	w.clock.advance(31 * time.Minute)

	_, ok = w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.EqualValues(t, 4, calls.Load(), "an older one asks again")
}

// Scenario (issue #623): no account of a runtime is logged in here. Nothing is known, so the provider's own list
// stands.
func TestNoAccountMeansNoServedModels(t *testing.T) {
	w := newClaudeWorld(t)

	names, ok := w.subs.ServedNames(t.Context(), "claude")
	assert.False(t, ok)
	assert.Empty(t, names)
}

// Scenario (issue #814, review of #832): two Codex accounts are served different models, as omp and CLIProxyAPI
// read Codex's list per account. Both lists are listed; a request for the model only the second account is served
// goes to it every time, and one for a model both have takes either, as before.
func TestCodexModelsAreListedAndRoutedPerAccount(t *testing.T) {
	w := newWorld(t)
	first := w.login()

	w.openai.set(func(o *openAI) { o.account = "second-chatgpt-account" })
	second := w.login()
	require.NotEqual(t, first.ID, second.ID)

	chatgpt := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Chatgpt-Account-Id") == "second-chatgpt-account" {
			_, _ = rw.Write([]byte(`{"models": [
				{"slug": "gpt-6.2-sol", "visibility": "list", "supported_in_api": true},
				{"slug": "gpt-review-second-only", "visibility": "list", "supported_in_api": true}]}`))

			return
		}

		_, _ = rw.Write([]byte(`{"models": [{"slug": "gpt-6.2-sol", "visibility": "list", "supported_in_api": true}]}`))
	}))
	t.Cleanup(chatgpt.Close)
	w.codex.ReadLimitsFrom(chatgpt.URL)

	names, ok := w.subs.ServedNames(t.Context(), "codex")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"gpt-6.2-sol", "gpt-review-second-only"}, names)

	for _, session := range []string{"", "", "conversation-1", "conversation-2"} {
		token, err := w.subs.TokenFor(t.Context(), "codex", "gpt-review-second-only", session)
		require.NoError(t, err)
		assert.Equal(t, second.ID, token.AccountID, "session %q", session)
	}

	var both []string

	for range 2 {
		token, err := w.subs.TokenFor(t.Context(), "codex", "gpt-6.2-sol", "")
		require.NoError(t, err)

		both = append(both, token.AccountID)
	}

	assert.ElementsMatch(t, []string{first.ID, second.ID}, both, "in turn, as without a model")
}

// Scenario (issue #814): with two Claude accounts, the list is the first account's, as before: neither omp nor
// CLIProxyAPI reads Anthropic's list per account, and a request for a Claude model may go to either account.
func TestAClaudeListIsTheFirstAccounts(t *testing.T) {
	w := newClaudeWorld(t)
	ids := w.loginN(2)

	var calls atomic.Int32

	w.oauth.answerModels(func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = rw.Write([]byte(`{"data": [{"id": "claude-opus-5-5"}], "has_more": false}`))
	})

	names, ok := w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.Equal(t, []string{"claude-opus-5-5"}, names)
	assert.EqualValues(t, 1, calls.Load(), "one account is asked")

	var picked []string

	for range 2 {
		token, err := w.subs.TokenFor(t.Context(), "claude", "claude-opus-5-5", "")
		require.NoError(t, err)

		picked = append(picked, token.AccountID)
	}

	assert.ElementsMatch(t, ids, picked)
}

// Scenario (review of #832, round 4): two Claude accounts; an hour later the first one's list cannot be read again.
// The list is the second account's, read now, as main read it, rather than the first one's old list.
func TestAClaudeListFallsToTheNextAccountWhenTheFirstFails(t *testing.T) {
	w := newClaudeWorld(t)
	w.loginN(2)

	var calls atomic.Int32

	w.oauth.answerModels(func(rw http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = rw.Write([]byte(`{"data": [{"id": "claude-old"}], "has_more": false}`))
		case 2:
			http.Error(rw, `{"error":{"type":"overloaded_error"}}`, http.StatusServiceUnavailable)
		default:
			_, _ = rw.Write([]byte(`{"data": [{"id": "claude-new"}], "has_more": false}`))
		}
	})

	names, ok := w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.Equal(t, []string{"claude-old"}, names)

	w.clock.advance(61 * time.Minute)

	names, ok = w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.Equal(t, []string{"claude-new"}, names, "the second account's list")
	assert.EqualValues(t, 3, calls.Load())

	// An hour on, neither account's list can be read: the last list answered stays, not the first account's older one.
	w.oauth.answerModels(func(rw http.ResponseWriter, _ *http.Request) {
		http.Error(rw, `{"error":{"type":"overloaded_error"}}`, http.StatusServiceUnavailable)
	})
	w.clock.advance(61 * time.Minute)

	names, ok = w.subs.ServedNames(t.Context(), "claude")
	require.True(t, ok)
	assert.Equal(t, []string{"claude-new"}, names, "the last list answered")
}
