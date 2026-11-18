package subscription_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// credentialsFile is Claude Code's .credentials.json for the tokens, its
// access token ending at expiresAt.
func credentialsFile(access, refresh string, expiresAt time.Time) []byte {
	return fmt.Appendf(nil, `{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,`+
		`"scopes":["user:inference"],"subscriptionType":"max"}}`, access, refresh, expiresAt.UnixMilli())
}

// Scenario: the owner imports a Claude Code session whose access token is
// still good. The account is who the profile says, its tokens are the file's,
// and nothing is refreshed, so the session is moved as it is (issue #828).
func TestAClaudeSessionIsImportedFromItsCredentialsFile(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	a, err := w.subs.Import(t.Context(), "claude",
		credentialsFile("sk-ant-oat-file", "sk-ant-ort-file", t0.Add(6*time.Hour)), "")
	require.NoError(t, err)

	assert.Equal(t, "owner@example.com", a.Label, "the e-mail comes from the profile, not the file")
	assert.Equal(t, "org-uuid-1", a.ExternalID)
	assert.Equal(t, subscription.StateActive, a.State)
	assert.Equal(t, t0.Add(6*time.Hour), *a.AccessTokenExpiresAt)

	token, err := w.subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat-file", token.AccessToken)

	w.oauth.mu.Lock()
	assert.Zero(t, w.oauth.refreshes, "a good access token is kept")
	w.oauth.mu.Unlock()
}

// Scenario: a file whose access token has ended is refreshed before the
// profile is read, and the account keeps the new tokens.
func TestAnEndedClaudeSessionIsRefreshedOnImport(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	_, err := w.subs.Import(t.Context(), "claude",
		credentialsFile("sk-ant-oat-old", "sk-ant-ort-old", t0.Add(-time.Hour)), "")
	require.NoError(t, err)

	w.oauth.mu.Lock()
	assert.Equal(t, 1, w.oauth.refreshes)
	w.oauth.mu.Unlock()

	token, err := w.subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat-2", token.AccessToken, "the refreshed access token")
}

// Scenario: importing the same account again updates it instead of adding a
// second one, as a second sign-in does.
func TestAClaudeSessionImportedTwiceIsOneAccount(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	first, err := w.subs.Import(t.Context(), "claude",
		credentialsFile("sk-ant-oat-a", "sk-ant-ort-a", t0.Add(6*time.Hour)), "")
	require.NoError(t, err)

	second, err := w.subs.Import(t.Context(), "claude",
		credentialsFile("sk-ant-oat-b", "sk-ant-ort-b", t0.Add(7*time.Hour)), "")
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID)

	accounts, err := w.subs.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, accounts, 1)

	token, err := w.subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat-b", token.AccessToken, "the newer tokens")
}

// Scenario: a file that is not Claude Code's credentials is refused as a bad
// paste, and no account is made.
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

	accounts, err := w.subs.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, accounts)
}
