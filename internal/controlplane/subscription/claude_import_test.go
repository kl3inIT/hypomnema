package subscription_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// credentialsFile is Claude Code's .credentials.json for the tokens.
func credentialsFile(access, refresh string) []byte {
	return fmt.Appendf(nil, `{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,`+
		`"scopes":["user:inference"],"subscriptionType":"max"}}`, access, refresh, t0.Add(6*time.Hour).UnixMilli())
}

// Scenario: the owner imports a Claude Code session (issue #828). Its refresh
// token is spent at once, so what is kept is the newest token set; the
// account is who Anthropic says, and its tokens are the refreshed ones.
func TestAClaudeSessionIsImportedFromItsCredentialsFile(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	a, err := w.subs.Import(t.Context(), "claude", credentialsFile("sk-ant-oat-file", "sk-ant-ort-file"), "")
	require.NoError(t, err)

	assert.Equal(t, "owner-1@example.com", a.Label, "the e-mail comes from Anthropic, not the file")
	assert.Equal(t, "org-uuid-1", a.ExternalID)
	assert.Equal(t, subscription.StateActive, a.State)
	assert.Equal(t, t0.Add(3600*time.Second), *a.AccessTokenExpiresAt)

	w.oauth.mu.Lock()
	assert.Equal(t, 1, w.oauth.refreshes, "the file's refresh token is spent on import")
	w.oauth.mu.Unlock()

	token, err := w.subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat-2", token.AccessToken, "the refreshed access token, not the file's")
}

// Scenario: importing the same account again updates it instead of adding a
// second one, as a second sign-in does.
func TestAClaudeSessionImportedTwiceIsOneAccount(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	first, err := w.subs.Import(t.Context(), "claude", credentialsFile("sk-ant-oat-a", "sk-ant-ort-a"), "")
	require.NoError(t, err)

	second, err := w.subs.Import(t.Context(), "claude", credentialsFile("sk-ant-oat-b", "sk-ant-ort-b"), "")
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID)

	accounts, err := w.subs.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, accounts, 1)
}

// Scenario: a file whose session was already used (its refresh token spent,
// as when the control plane refreshed an earlier import of it) is refused,
// and nothing is kept: an older token set never replaces a newer one.
func TestAClaudeSessionAlreadyUsedIsRefused(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	_, err := w.subs.Import(t.Context(), "claude", credentialsFile("sk-ant-oat-old", "sk-ant-ort-spent"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign in on that machine again")

	accounts, err := w.subs.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, accounts)
}

// Scenario: when Anthropic does not say whose the session is, the import is
// refused rather than kept under an id made up from its token, which could
// make a second account of one already kept.
func TestAClaudeSessionOfNobodyKnownIsNotKept(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	w.oauth.mu.Lock()
	w.oauth.anonymous = true
	w.oauth.mu.Unlock()

	_, err := w.subs.Import(t.Context(), "claude", credentialsFile("sk-ant-oat-file", "sk-ant-ort-file"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not say whose this session is")

	accounts, err := w.subs.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, accounts)
}

// Scenario: a file that is not Claude Code's credentials is refused as a bad
// paste, before anything reaches Anthropic.
func TestAFileWithoutClaudeCredentialsIsRefused(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	for _, raw := range []string{
		`not json`,
		`{"other":{}}`,
		`{"claudeAiOauth":{"accessToken":"sk-ant-oat-1"}}`,
	} {
		_, err := w.subs.Import(t.Context(), "claude", []byte(raw), "")
		require.ErrorIs(t, err, subscription.ErrBadPaste, raw)
	}

	w.oauth.mu.Lock()
	assert.Zero(t, w.oauth.refreshes)
	w.oauth.mu.Unlock()
}
