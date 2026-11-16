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
		assert.NotEmpty(t, r.URL.Query().Get("client_version"))
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		assert.Equal(t, a.ExternalID, r.Header.Get("Chatgpt-Account-Id"))

		_, _ = rw.Write([]byte(`{"models": [
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
	assert.Equal(t, []string{"gpt-6.1-sol", "gpt-6.1-terra"}, names)
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
