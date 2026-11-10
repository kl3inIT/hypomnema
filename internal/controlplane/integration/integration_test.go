package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const (
	clientID     = "1234-example.apps.googleusercontent.com"
	clientSecret = "client-secret-never-printed"
	ownerEmail   = "owner@example.com"
	scopeBase    = "https://www.googleapis.com/auth/"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// google stands in for Google's token, user and revoke endpoints. It checks
// what a real one checks: the client, the PKCE verifier against the challenge
// the consent page was opened with, and that a refresh token is one it gave.
type google struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// challenge is the PKCE challenge of the sign-in in progress.
	challenge string
	// allowed are the scopes the owner leaves ticked on the consent page;
	// nil allows what was asked.
	allowed []string
	asked   []string
	// refuseRefresh makes every refresh answer invalid_grant.
	refuseRefresh bool
	// revokeDown makes the revoke endpoint fail.
	revokeDown bool

	issued    int
	refreshes int
	revoked   []string
}

func newGoogle(t *testing.T) *google {
	t.Helper()

	g := &google{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", g.token)
	mux.HandleFunc("GET /userinfo", g.userInfo)
	mux.HandleFunc("POST /revoke", g.revoke)
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)

	return g
}

func (g *google) definition() integration.Definition {
	d := integration.Google()
	d.AuthURL, d.TokenURL = g.srv.URL+"/auth", g.srv.URL+"/token"
	d.UserInfoURL, d.RevokeURL = g.srv.URL+"/userinfo", g.srv.URL+"/revoke"

	return d
}

// consent is the owner opening the link and allowing it: it returns the
// address the browser ends on.
func (g *google) consent(link string) string {
	g.t.Helper()

	u, err := url.Parse(link)
	require.NoError(g.t, err)

	q := u.Query()
	assert.Equal(g.t, clientID, q.Get("client_id"))
	assert.Equal(g.t, "offline", q.Get("access_type"))
	assert.Equal(g.t, "S256", q.Get("code_challenge_method"))
	assert.Equal(g.t, "http://localhost:1", q.Get("redirect_uri"))

	g.mu.Lock()
	g.challenge, g.asked = q.Get("code_challenge"), strings.Fields(q.Get("scope"))
	g.mu.Unlock()

	return "http://localhost:1/?state=" + q.Get("state") + "&code=the-code&scope=ignored"
}

func (g *google) token(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if r.PostFormValue("client_id") != clientID || r.PostFormValue("client_secret") != clientSecret {
		g.answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})

		return
	}

	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if r.PostFormValue("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			g.answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})

			return
		}

		g.challenge = "" // A code is spent by its first exchange.
		g.issued++

		scopes := g.asked
		if g.allowed != nil {
			scopes = g.allowed
		}

		g.answer(w, http.StatusOK, map[string]any{
			"access_token": fmt.Sprintf("access-%d", g.issued), "refresh_token": "refresh-1", "expires_in": 3600,
			"scope": strings.Join(scopes, " "),
		})
	case "refresh_token":
		g.refreshes++

		if g.refuseRefresh || r.PostFormValue("refresh_token") != "refresh-1" {
			g.answer(w, http.StatusBadRequest, map[string]any{
				"error": "invalid_grant", "error_description": "Token has been expired or revoked.",
			})

			return
		}

		g.issued++
		// Google sends no new refresh token with a refresh.
		g.answer(w, http.StatusOK, map[string]any{
			"access_token": fmt.Sprintf("access-%d", g.issued), "expires_in": 3600,
		})
	default:
		g.answer(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
	}
}

func (g *google) userInfo(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer access-") {
		g.answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_token"})

		return
	}

	g.answer(w, http.StatusOK, map[string]any{"id": "1", "email": ownerEmail})
}

func (g *google) revoke(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.revokeDown {
		g.answer(w, http.StatusServiceUnavailable, map[string]any{})

		return
	}

	g.revoked = append(g.revoked, r.PostFormValue("token"))
	g.answer(w, http.StatusOK, map[string]any{})
}

func (g *google) answer(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	assert.NoError(g.t, json.NewEncoder(w).Encode(body))
}

func (g *google) counts() (issued, refreshes int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.issued, g.refreshes
}

type world struct {
	t      *testing.T
	path   string
	google *google
	clock  *fakeClock
	orch   *orchestration.Service
	vault  *vault.Service
	svc    *integration.Service
}

func newWorld(t *testing.T) *world {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hypomnema.db")

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &world{t: t, path: path, google: newGoogle(t), clock: &fakeClock{now: t0}}
	w.orch = orchestration.New(db, w.clock, platform.NewID)
	// A Connection and a client name their secret plainly, as the control plane's wiring has it.
	w.vault = vault.New(db, w.clock, platform.NewID, keys, func(ctx context.Context, ref string) ([]string, error) {
		name, _ := vault.NameFromRef(ref)

		return w.orch.ConnectionsUsingSecret(ctx, name)
	})
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, w.google.definition())

	return w
}

// signIn is the owner's whole sign-in: start, consent in the browser, paste.
func (w *world) signIn(label string, services ...string) (integration.Connection, error) {
	w.t.Helper()

	started, err := w.svc.Begin(w.t.Context(), integration.GoogleID, label, services)
	require.NoError(w.t, err)

	return w.svc.Complete(w.t.Context(), integration.GoogleID, started.ID, w.google.consent(started.URL))
}

func (w *world) withClient() *world {
	w.t.Helper()
	require.NoError(w.t, w.svc.SetClient(w.t.Context(), integration.GoogleID, clientID, clientSecret))

	return w
}

func (w *world) theGoogle() integration.Integration {
	w.t.Helper()

	all, err := w.svc.List(w.t.Context())
	require.NoError(w.t, err)
	require.Len(w.t, all, 1)

	return all[0]
}

// stored is the database as it is on disk, to look for a value that must not
// be there in the clear.
func (w *world) stored() string {
	w.t.Helper()

	var b strings.Builder

	for _, file := range []string{w.path, w.path + "-wal"} {
		raw, err := os.ReadFile(file) //nolint:gosec // this test's own database
		if err == nil {
			b.Write(raw)
		}
	}

	require.NotZero(w.t, b.Len())

	return b.String()
}

// Scenario: the owner gives the Google client, signs his work account in for
// Gmail to read and Calendar to read and write, and an agent's token is asked
// for over the next hours. The sign-in asks Google for exactly those scopes;
// the Connection carries the account's email and what was granted; the tokens
// and the client's secret are sealed; a token is refreshed only once it is
// about to end, and Google's refresh token is kept when it sends no new one.
func TestTheOwnerSignsAGoogleAccountInAndItsTokenStaysFresh(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient()

	started, err := w.svc.Begin(t.Context(), integration.GoogleID, "work", []string{"calendar", "gmail:read"})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, started.ExpiresIn)

	callback := w.google.consent(started.URL)
	assert.ElementsMatch(t, []string{
		scopeBase + "userinfo.email", scopeBase + "gmail.readonly", scopeBase + "calendar",
	}, w.google.asked)

	conn, err := w.svc.Complete(t.Context(), integration.GoogleID, started.ID, callback)
	require.NoError(t, err)
	assert.Equal(t, ownerEmail, conn.Account)
	assert.Equal(t, []integration.Grant{{Service: "gmail", ReadOnly: true}, {Service: "calendar"}}, conn.Grants)
	assert.False(t, conn.SignInRequired)

	listed := w.theGoogle()
	assert.Equal(t, clientID, listed.ClientID)
	require.Len(t, listed.Connections, 1)
	assert.Equal(t, "work", listed.Connections[0].Label)

	stored := w.stored()
	assert.Contains(t, stored, ownerEmail)
	assert.NotContains(t, stored, clientSecret)
	assert.NotContains(t, stored, "refresh-1")
	assert.NotContains(t, stored, "access-1")

	token, err := w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.NoError(t, err)
	assert.Equal(t, "access-1", token.AccessToken)
	assert.Equal(t, t0.Add(time.Hour), token.ExpiresAt)

	w.clock.advance(57 * time.Minute)

	token, err = w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.NoError(t, err)
	assert.Equal(t, "access-1", token.AccessToken, "three minutes are left: not refreshed yet")

	w.clock.advance(2 * time.Minute)

	token, err = w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.NoError(t, err)
	assert.Equal(t, "access-2", token.AccessToken)

	w.clock.advance(2 * time.Hour)

	token, err = w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.NoError(t, err)
	assert.Equal(t, "access-3", token.AccessToken, "the refresh token of the sign-in still works")

	_, refreshes := w.google.counts()
	assert.Equal(t, 2, refreshes)
}

// Scenario: on the consent page the owner unticks Calendar and lets Gmail
// only be read. The Connection records what Google gave, not what was asked.
func TestAConnectionRecordsWhatTheConsentPageAllowed(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient()
	w.google.allowed = []string{scopeBase + "userinfo.email", scopeBase + "gmail.readonly"}

	conn, err := w.signIn("personal", "gmail", "calendar")
	require.NoError(t, err)
	assert.Equal(t, []integration.Grant{{Service: "gmail", ReadOnly: true}}, conn.Grants)

	// With nothing allowed there is no Connection to keep.
	w.google.allowed = []string{scopeBase + "userinfo.email"}

	_, err = w.signIn("other", "drive")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Len(t, w.theGoogle().Connections, 1)
}

// Scenario: the owner removes Hypomnema's access in his Google account. The
// next token asked for is refused for good and the Connection shows it needs
// a sign-in, without asking Google again each time; signing the same label in
// again makes it work.
func TestARefusedRefreshAsksForASignIn(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient()

	_, err := w.signIn("work", "gmail:read")
	require.NoError(t, err)

	w.clock.advance(2 * time.Hour)
	w.google.refuseRefresh = true

	_, err = w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.NotContains(t, err.Error(), "refresh-1")
	assert.True(t, w.theGoogle().Connections[0].SignInRequired)

	_, err = w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.ErrorIs(t, err, integration.ErrSignInRequired)

	_, refreshes := w.google.counts()
	assert.Equal(t, 1, refreshes)

	w.google.refuseRefresh = false

	conn, err := w.signIn("work", "gmail:read", "drive:read")
	require.NoError(t, err)
	assert.False(t, conn.SignInRequired)
	assert.Equal(t, []integration.Grant{{Service: "gmail", ReadOnly: true}, {Service: "drive", ReadOnly: true}},
		conn.Grants)
	assert.Len(t, w.theGoogle().Connections, 1)

	token, err := w.svc.Token(t.Context(), integration.GoogleID, "work")
	require.NoError(t, err)
	assert.Equal(t, "access-2", token.AccessToken)
}

// Scenario: sign-ins that must not be completed: an address from another
// sign-in, an address Google sent a refusal to, one completed already, and
// one started more than thirty minutes ago.
func TestASignInIsCompletedOnceWithItsOwnAddress(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient()
	ctx := t.Context()

	first, err := w.svc.Begin(ctx, integration.GoogleID, "work", []string{"gmail"})
	require.NoError(t, err)
	second, err := w.svc.Begin(ctx, integration.GoogleID, "work", []string{"gmail"})
	require.NoError(t, err)

	_, err = w.svc.Complete(ctx, integration.GoogleID, first.ID, w.google.consent(second.URL))
	require.ErrorIs(t, err, integration.ErrSignInFailed)

	_, err = w.svc.Complete(ctx, integration.GoogleID, first.ID, "http://localhost:1/?error=access_denied")
	require.ErrorIs(t, err, integration.ErrSignInFailed)

	_, err = w.svc.Complete(ctx, integration.GoogleID, second.ID, w.google.consent(second.URL))
	require.NoError(t, err)

	_, err = w.svc.Complete(ctx, integration.GoogleID, second.ID, w.google.consent(second.URL))
	require.ErrorIs(t, err, integration.ErrNotFound)

	w.clock.advance(31 * time.Minute)

	_, err = w.svc.Complete(ctx, integration.GoogleID, first.ID, w.google.consent(first.URL))
	require.ErrorIs(t, err, integration.ErrNotFound)

	issued, _ := w.google.counts()
	assert.Equal(t, 1, issued)
}

// Scenario: what a sign-in cannot be started with.
func TestASignInNeedsAClientALabelAndKnownServices(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.Begin(ctx, integration.GoogleID, "work", []string{"gmail"})
	require.ErrorIs(t, err, orchestration.ErrConflict)
	assert.Contains(t, err.Error(), "integrations client set google")

	w.withClient()

	for name, tc := range map[string]struct {
		integration, label string
		services           []string
		want               error
	}{
		"an integration that does not exist": {"slack", "work", []string{"gmail"}, integration.ErrNotFound},
		"a label with a space":               {integration.GoogleID, "my work", []string{"gmail"}, integration.ErrInvalid},
		"no service":                         {integration.GoogleID, "work", nil, integration.ErrInvalid},
		"a service Google has not":           {integration.GoogleID, "work", []string{"slides"}, integration.ErrInvalid},
		"a mode that is not read":            {integration.GoogleID, "work", []string{"gmail:write"}, integration.ErrInvalid},
		"a service named twice":              {integration.GoogleID, "work", []string{"gmail", "gmail:read"}, integration.ErrInvalid},
	} {
		_, err := w.svc.Begin(ctx, tc.integration, tc.label, tc.services)
		require.ErrorIs(t, err, tc.want, name)
	}

	require.ErrorIs(t, w.svc.SetClient(ctx, integration.GoogleID, clientID, "two words"), integration.ErrInvalid)
	require.ErrorIs(t, w.svc.SetClient(ctx, "slack", clientID, clientSecret), integration.ErrNotFound)
}

// Scenario: the owner disconnects an account. Google is asked to end the
// tokens and the Connection and its secret are gone; when Google cannot be
// reached they are gone all the same and the owner is told the access was not
// ended there. The client's secret cannot be deleted from the vault while the
// Integration uses it.
func TestDisconnectingEndsTheTokensAndForgetsThem(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient()
	ctx := t.Context()

	_, err := w.signIn("work", "gmail")
	require.NoError(t, err)
	_, err = w.signIn("personal", "calendar:read")
	require.NoError(t, err)

	revoked, err := w.svc.Disconnect(ctx, integration.GoogleID, "work")
	require.NoError(t, err)
	assert.True(t, revoked)
	assert.Equal(t, []string{"refresh-1"}, w.google.revoked)

	w.google.revokeDown = true

	revoked, err = w.svc.Disconnect(ctx, integration.GoogleID, "personal")
	require.NoError(t, err)
	assert.False(t, revoked)

	assert.Empty(t, w.theGoogle().Connections)

	_, err = w.svc.Token(ctx, integration.GoogleID, "work")
	require.ErrorIs(t, err, integration.ErrNotFound)

	_, err = w.svc.Disconnect(ctx, integration.GoogleID, "work")
	require.ErrorIs(t, err, integration.ErrNotFound)

	secrets, err := w.vault.List(ctx)
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	assert.Equal(t, "integration-google-client", secrets[0].Name)
	require.ErrorIs(t, w.vault.Delete(ctx, secrets[0].Name), vault.ErrInUse)
}

// Scenario: a node is told to have Google's command-line tool only once a
// Google account is connected, and only when the tool has a release for the
// node's system. Every release is pinned by a checksum and fetched over HTTPS.
func TestNodesAreToldToHaveTheToolOfAConnectedIntegration(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient()
	ctx := t.Context()

	assert.Empty(t, w.svc.Tools(ctx, "linux", "amd64"), "nobody signed in: no node needs the tool")

	_, err := w.signIn("work", "gmail:read")
	require.NoError(t, err)

	tools := w.svc.Tools(ctx, "linux", "amd64")
	require.Len(t, tools, 1)
	assert.Equal(t, "gws", tools[0].Name)
	assert.Equal(t, "gws", tools[0].Binary)
	assert.NotEmpty(t, tools[0].Version)

	arm := w.svc.Tools(ctx, "linux", "arm64")
	require.Len(t, arm, 1)
	assert.NotEqual(t, tools[0].SHA256, arm[0].SHA256, "each system has its own release file")

	assert.Empty(t, w.svc.Tools(ctx, "windows", "amd64"), "no release for that system")

	for system, release := range integration.Google().Tool.Releases {
		assert.Regexp(t, "^[0-9a-f]{64}$", release.SHA256, system)
		assert.Regexp(t, "^https://github.com/googleworkspace/cli/releases/download/v", release.URL, system)
	}

	_, err = w.svc.Disconnect(ctx, integration.GoogleID, "work")
	require.NoError(t, err)
	assert.Empty(t, w.svc.Tools(ctx, "linux", "amd64"))
}
