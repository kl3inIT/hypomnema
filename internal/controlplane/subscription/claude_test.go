package subscription_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// anthropicOAuth stands in for console.anthropic.com's OAuth token endpoint and
// api.anthropic.com's profile: it spends a refresh token on each use.
type anthropicOAuth struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	exchanges int
	refreshes int
	lastGrant string
}

func newAnthropicOAuth(t *testing.T) *anthropicOAuth {
	t.Helper()

	a := &anthropicOAuth{t: t}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		a.mu.Lock()
		a.lastGrant = body["grant_type"]

		var access, refresh, uuid, email string

		switch body["grant_type"] {
		case "authorization_code":
			a.exchanges++
			// Each login is a distinct account, so a test can log in several.
			access = fmt.Sprintf("sk-ant-oat-%d", a.exchanges)
			refresh = fmt.Sprintf("sk-ant-ort-%d", a.exchanges)
			uuid = fmt.Sprintf("acc-uuid-%d", a.exchanges)
			email = fmt.Sprintf("owner-%d@example.com", a.exchanges)
		case "refresh_token":
			a.refreshes++
			access, refresh, uuid, email = "sk-ant-oat-2", "sk-ant-ort-2", "acc-uuid-1", "owner-1@example.com"
		}
		a.mu.Unlock()

		writeJSON(w, map[string]any{
			"access_token": access, "refresh_token": refresh, "expires_in": 3600,
			"account":      map[string]string{"uuid": uuid, "email_address": email},
			"organization": map[string]string{"name": "Max"},
		})
	})

	mux.HandleFunc("GET /api/oauth/profile", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"account":      map[string]string{"uuid": "acc-uuid-1", "email_address": "owner@example.com"},
			"organization": map[string]string{"name": "Max"},
		})
	})

	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)

	return a
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	//nolint:errchkjson // a test map of strings and ints always encodes
	_ = json.NewEncoder(w).Encode(v)
}

type claudeWorld struct {
	t     *testing.T
	oauth *anthropicOAuth
	clock *fakeClock
	subs  *subscription.Service
}

func newClaudeWorld(t *testing.T) *claudeWorld {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &claudeWorld{t: t, oauth: newAnthropicOAuth(t), clock: &fakeClock{now: t0}}
	orch := orchestration.New(db, w.clock, platform.NewID)
	vlt := vault.New(db, w.clock, platform.NewID, keys, orch.AccountsUsingSecretRef)

	vendor := subscription.NewClaude(subscription.ClaudeOptions{
		AuthBase: w.oauth.srv.URL, TokenBase: w.oauth.srv.URL, ProfileBase: w.oauth.srv.URL,
	}, w.clock)
	w.subs = subscription.New(orch, vlt, w.clock, slog.New(slog.DiscardHandler), vendor)

	return w
}

// login goes through the paste flow: the link, then `code#state` as the page
// shows it.
func (w *claudeWorld) login() (subscription.Account, error) {
	w.t.Helper()

	attempt, err := w.subs.Begin(w.t.Context(), "claude")
	require.NoError(w.t, err)
	require.True(w.t, attempt.Paste)

	link, err := url.Parse(attempt.URL)
	require.NoError(w.t, err)

	return w.subs.Complete(w.t.Context(), attempt, "the-code#"+link.Query().Get("state"), "")
}

// Scenario: the owner logs a Claude subscription in. The consent link is Claude
// Code's own OAuth client, with PKCE; after the pasted code the account exists
// with its email and the tokens are in the vault, and Token gives the access
// token the gateway sends.
func TestOwnerLogsAClaudeSubscriptionIn(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	attempt, err := w.subs.Begin(t.Context(), "claude")
	require.NoError(t, err)

	link, err := url.Parse(attempt.URL)
	require.NoError(t, err)

	q := link.Query()
	assert.Equal(t, "9d1c250a-e61b-44d9-88ed-5944d1962f5e", q.Get("client_id"), "Claude Code's client")
	assert.Equal(t, "true", q.Get("code"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.NotEmpty(t, q.Get("code_challenge"))
	assert.Contains(t, q.Get("scope"), "user:inference")

	a, err := w.login()
	require.NoError(t, err)

	assert.Equal(t, "claude", a.Runtime)
	assert.Equal(t, "owner-1@example.com", a.Label)
	assert.Equal(t, "acc-uuid-1", a.ExternalID)
	assert.Equal(t, subscription.StateActive, a.State)
	assert.Equal(t, t0.Add(3600*time.Second), *a.AccessTokenExpiresAt)

	token, err := w.subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat-1", token.AccessToken)
}

// Scenario: a paste that belongs to another login is refused, so a stale code
// cannot be finished against a new attempt.
func TestAClaudePasteFromAnotherLoginIsRefused(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	attempt, err := w.subs.Begin(t.Context(), "claude")
	require.NoError(t, err)

	_, err = w.subs.Complete(t.Context(), attempt, "the-code#not-the-state", "")
	require.Error(t, err)
}

// Scenario: the access token is refreshed before it ends, spending the refresh
// token, and Token then gives the new access token.
func TestAClaudeTokenIsRefreshed(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)

	a, err := w.login()
	require.NoError(t, err)

	_, err = w.subs.Refresh(t.Context(), a.ID)
	require.NoError(t, err)

	w.oauth.mu.Lock()
	assert.Equal(t, 1, w.oauth.refreshes)
	w.oauth.mu.Unlock()

	token, err := w.subs.Token(t.Context(), "claude", "")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat-2", token.AccessToken, "the refreshed access token")
}

// loginN logs n distinct accounts in and returns their ids.
func (w *claudeWorld) loginN(n int) []string {
	w.t.Helper()

	ids := make([]string, 0, n)

	for range n {
		a, err := w.login()
		require.NoError(w.t, err)
		ids = append(ids, a.ID)
	}

	return ids
}

func (w *claudeWorld) account(session string, skip ...string) string {
	w.t.Helper()

	token, err := w.subs.Token(w.t.Context(), "claude", session, skip...)
	require.NoError(w.t, err)

	return token.AccountID
}

// Scenario: one conversation keeps one account across its requests, so the
// account's prompt cache stays warm.
func TestAClaudeSessionKeepsItsAccount(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	w.loginN(3)

	home := w.account("conversation-1")
	assert.Equal(t, home, w.account("conversation-1"), "the same session returns the same account")
	assert.Equal(t, home, w.account("conversation-1"), "and again")
}

// Scenario: requests with no session are spread round-robin over the accounts,
// so no one account takes all the load.
func TestClaudeAccountsRoundRobinWithoutASession(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	ids := w.loginN(3)

	seen := map[string]bool{}
	for range ids {
		seen[w.account("")] = true
	}

	assert.Len(t, seen, 3, "three requests reached three accounts")
}

// Scenario: while a session's home account is cooling, the session is served by
// another; when the home recovers, the session returns to it (its cache again).
func TestAClaudeSessionDetoursWhileItsHomeCools(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	w.loginN(3)

	home := w.account("conversation-1")

	require.NoError(t, w.subs.CoolDown(t.Context(), "claude", home, w.clock.Now().Add(time.Hour), "test"))

	detour := w.account("conversation-1")
	assert.NotEqual(t, home, detour, "a cooling home is stepped over")

	w.clock.advance(2 * time.Hour)
	assert.Equal(t, home, w.account("conversation-1"), "the session returns home once it is free")
}
