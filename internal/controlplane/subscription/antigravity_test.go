package subscription_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
)

const (
	ideSecret   = "ide-secret-never-printed"
	googleEmail = "owner@gmail.example"
)

// google stands in for Google: the token endpoint, the user's profile, the
// IDE's update manifest and two Cloud Code hosts.
type google struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// project is the Cloud Code project the account has; empty: none yet.
	project string
	// verify, when set, is where Google wants the account verified.
	verify string
	// dailyRefuses makes the daily host answer 403 to loadCodeAssist.
	dailyRefuses bool
	// revoked makes the refresh token refused for good.
	revoked bool
	// user is who logs in next: Google's id and the email.
	user  string
	email string

	issued     int
	operations int
	agents     []string
	extra      []string
	hosts      []string
}

func newGoogle(t *testing.T) *google {
	t.Helper()

	g := &google{t: t, project: "bright-wave-1234", user: "1122334455", email: googleEmail}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", g.token)
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		g.seen(r, "")

		g.mu.Lock()
		defer g.mu.Unlock()

		_, _ = fmt.Fprintf(w, `{"id":%q,"email":%q}`, g.user, g.email)
	})
	mux.HandleFunc("GET /manifest.yml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "version: 2.31.4\nfiles:\n  - url: Antigravity.zip\n")
	})

	for _, host := range []string{"daily", "prod"} {
		mux.HandleFunc("POST /"+host+"/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
			g.loadCodeAssist(w, r, host)
		})
		mux.HandleFunc("POST /"+host+"/v1internal:fetchAvailableModels", func(w http.ResponseWriter, r *http.Request) {
			g.seen(r, host)

			var in map[string]string
			if json.NewDecoder(r.Body).Decode(&in) != nil || in["project"] != "bright-wave-1234" {
				http.Error(w, `{"error":{"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)

				return
			}

			_, _ = io.WriteString(w, `{"models":{
			  "gemini-3.8-flash-high":{"quotaInfo":{"remainingFraction":0.4,"resetTime":"2026-11-10T14:00:00Z"}},
			  "claude-sonnet-5-5":{"quotaInfo":{"remainingFraction":1}},
			  "chat_internal_probe":{"isInternal":true},
			  "gpt-oss-120b-medium":{}}}`)
		})
		mux.HandleFunc("POST /"+host+"/v1internal:onboardUser", func(w http.ResponseWriter, r *http.Request) {
			g.seen(r, host)
			_, _ = io.WriteString(w, `{"name":"operations/onboard-1","done":false}`)
		})
		mux.HandleFunc("GET /"+host+"/v1internal/operations/onboard-1", func(w http.ResponseWriter, r *http.Request) {
			g.operation(w, r, host)
		})
	}

	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)

	return g
}

func (g *google) seen(r *http.Request, host string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.agents = append(g.agents, r.Header.Get("User-Agent"))
	g.hosts = append(g.hosts, host)

	for _, name := range []string{"X-Goog-Api-Client", "Client-Metadata", "X-Goog-User-Project"} {
		if r.Header.Get(name) != "" {
			g.extra = append(g.extra, name)
		}
	}
}

func (g *google) token(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := r.ParseForm(); err != nil || r.PostForm.Get("client_secret") != ideSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)

		return
	}

	refresh := ""

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if r.PostForm.Get("code") != "the-code" || r.PostForm.Get("redirect_uri") != "http://localhost:51121/oauth-callback" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)

			return
		}

		refresh = `"refresh_token":"google-refresh",`
	case "refresh_token":
		if g.revoked || r.PostForm.Get("refresh_token") != "google-refresh" {
			http.Error(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`,
				http.StatusBadRequest)

			return
		}
	}

	g.issued++

	_, _ = fmt.Fprintf(w, `{"access_token":"ya29.access-%d",%s"expires_in":3599}`, g.issued, refresh)
}

func (g *google) loadCodeAssist(w http.ResponseWriter, r *http.Request, host string) {
	g.seen(r, host)

	g.mu.Lock()
	defer g.mu.Unlock()

	var in struct {
		Metadata map[string]string `json:"metadata"`
	}

	switch {
	case json.NewDecoder(r.Body).Decode(&in) != nil || in.Metadata["ideType"] != "ANTIGRAVITY":
		http.Error(w, `{"error":{"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
	case host == "daily" && g.dailyRefuses:
		http.Error(w, `{"error":{"status":"PERMISSION_DENIED","message":"not here"}}`, http.StatusForbidden)
	case g.verify != "":
		_, _ = fmt.Fprintf(w, `{"ineligibleTiers":[{"reasonCode":"VALIDATION_REQUIRED",`+
			`"reasonMessage":"Verify your account","validationUrl":%q}]}`, g.verify)
	case g.project == "":
		_, _ = io.WriteString(w, `{"allowedTiers":[{"id":"legacy-tier"},{"id":"free-tier","isDefault":true}]}`)
	default:
		_, _ = fmt.Fprintf(w, `{"cloudaicompanionProject":%q,"currentTier":{"id":"free-tier"},`+
			`"paidTier":{"id":"g1-pro-tier"}}`, g.project)
	}
}

func (g *google) operation(w http.ResponseWriter, r *http.Request, host string) {
	g.seen(r, host)

	g.mu.Lock()
	defer g.mu.Unlock()

	g.operations++
	if g.operations < 3 {
		_, _ = io.WriteString(w, `{"name":"operations/onboard-1","done":false}`)

		return
	}

	_, _ = io.WriteString(w, `{"name":"operations/onboard-1","done":true,`+
		`"response":{"cloudaicompanionProject":{"id":"made-for-you-77"}}}`)
}

func (g *google) set(change func(*google)) {
	g.mu.Lock()
	defer g.mu.Unlock()

	change(g)
}

type antigravityWorld struct {
	t      *testing.T
	google *google
	clock  *fakeClock
	vault  *vault.Service
	subs   *subscription.Service
}

func newAntigravityWorld(t *testing.T, secret string) *antigravityWorld {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &antigravityWorld{t: t, google: newGoogle(t), clock: &fakeClock{now: t0}}
	orch := orchestration.New(db, w.clock, platform.NewID)
	w.vault = vault.New(db, w.clock, platform.NewID, keys, orch.AccountsUsingSecretRef)

	base := w.google.srv.URL
	vendor := subscription.NewAntigravity(subscription.AntigravityOptions{
		ClientSecret: secret, AuthURL: base + "/auth", TokenURL: base + "/token", UserInfoURL: base + "/userinfo",
		CodeAssistHosts: []string{base + "/daily", base + "/prod"}, ManifestURL: base + "/manifest.yml",
	}, w.clock)
	vendor.PollWithoutWaiting()
	w.subs = subscription.New(orch, w.vault, w.clock, slog.New(slog.DiscardHandler), vendor)

	return w
}

// login goes through Google's consent as the owner does: the link, then the
// address the browser ended on.
func (w *antigravityWorld) login() (subscription.Account, error) {
	w.t.Helper()

	attempt, err := w.subs.Begin(w.t.Context(), "antigravity")
	require.NoError(w.t, err)
	require.True(w.t, attempt.Paste)

	link, err := url.Parse(attempt.URL)
	require.NoError(w.t, err)

	return w.subs.Complete(w.t.Context(), attempt,
		"http://localhost:51121/oauth-callback?state="+link.Query().Get("state")+"&code=the-code&scope=email", "")
}

// Scenario: the owner logs an Antigravity account in. Google's consent page
// asks for the IDE's scopes and offline access; after the pasted address the
// account exists with its email, its tier and the Cloud Code project it works
// in, and its tokens are in the vault. Google is called as the IDE calls it:
// its user agent at the version of the update manifest, and none of the
// headers that make the backend refuse.
func TestOwnerLogsAnAntigravityAccountIn(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	attempt, err := w.subs.Begin(t.Context(), "antigravity")
	require.NoError(t, err)

	link, err := url.Parse(attempt.URL)
	require.NoError(t, err)

	q := link.Query()
	assert.Equal(t, "offline", q.Get("access_type"))
	assert.Equal(t, "consent", q.Get("prompt"))
	assert.Equal(t, "http://localhost:51121/oauth-callback", q.Get("redirect_uri"))
	assert.Contains(t, q.Get("scope"), "https://www.googleapis.com/auth/cloud-platform")
	assert.NotContains(t, attempt.URL, ideSecret)

	a, err := w.login()
	require.NoError(t, err)

	assert.Equal(t, "antigravity", a.Runtime)
	assert.Equal(t, googleEmail, a.Label)
	assert.Equal(t, "1122334455", a.ExternalID)
	assert.Equal(t, "bright-wave-1234", a.Project)
	assert.Equal(t, "g1-pro-tier", a.Plan)
	assert.Equal(t, subscription.StateActive, a.State)
	assert.Equal(t, t0.Add(3599*time.Second), *a.AccessTokenExpiresAt)

	token, err := w.subs.Token(t.Context(), "antigravity")
	require.NoError(t, err)
	assert.Equal(t, "ya29.access-1", token.AccessToken)
	assert.Equal(t, "bright-wave-1234", token.Project)

	w.google.set(func(g *google) {
		assert.Equal(t, []string{"", "daily"}, g.hosts)
		assert.Empty(t, g.extra)

		for _, agent := range g.agents {
			assert.Equal(t, "antigravity/hub/2.31.4 (aidev_client; os_type=darwin; arch=arm64)", agent)
		}
	})
}

// Scenario: an account that never used Antigravity has no project. Google is
// asked to make one for the default tier, the operation is followed until it
// is done, and the login ends with the new project. An account Google first
// wants verified is not created: the owner is given the link.
func TestAnAccountWithNoProjectGetsOneOrIsToldToVerify(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.google.set(func(g *google) { g.project = "" })

	a, err := w.login()
	require.NoError(t, err)
	assert.Equal(t, "made-for-you-77", a.Project)
	assert.Equal(t, "free-tier", a.Plan)

	blocked := newAntigravityWorld(t, ideSecret)
	blocked.google.set(func(g *google) { g.verify = "https://accounts.google.com/verify?x=1" })

	_, err = blocked.login()
	require.ErrorIs(t, err, subscription.ErrLoginFailed)

	var verify *subscription.VerificationError
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, "https://accounts.google.com/verify?x=1", verify.URL)

	all, err := blocked.subs.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, all)
}

// Scenario: the daily host does not know the account. The other host is
// asked and answers; the login succeeds.
func TestTheProjectIsFoundOnTheOtherHost(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.google.set(func(g *google) { g.dailyRefuses = true })

	a, err := w.login()
	require.NoError(t, err)
	assert.Equal(t, "bright-wave-1234", a.Project)

	w.google.set(func(g *google) { assert.Equal(t, []string{"", "daily", "prod"}, g.hosts) })
}

// Scenario: an Antigravity token is refreshed. Google keeps the refresh
// token, so the same one is stored again; when Google has revoked it the
// account asks for a new login. Without the IDE's client secret in the
// configuration no login can start, and the error says what to set.
func TestAnAntigravityTokenIsRefreshedUntilGoogleRevokesIt(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	a, err := w.login()
	require.NoError(t, err)

	w.clock.advance(45 * time.Minute)
	require.NoError(t, w.subs.RefreshDue(t.Context()))

	name, _ := vault.NameFromRef(a.SecretRef)
	opened, err := w.vault.Open(t.Context(), name)
	require.NoError(t, err)
	assert.Equal(t, "ya29.access-2", opened.Payload[vault.KeyAccessToken])
	assert.Equal(t, "google-refresh", opened.Payload[vault.KeyRefreshToken])

	w.google.set(func(g *google) { g.revoked = true })

	_, err = w.subs.Refresh(t.Context(), a.ID)
	require.ErrorIs(t, err, vault.ErrLoginRequired)

	after, err := w.subs.Get(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, subscription.StateLoginRequired, after.State)
	assert.Contains(t, after.Refresh.Error, "invalid_grant")
	assert.Equal(t, "bright-wave-1234", after.Project, "what the login found stays")

	unset := newAntigravityWorld(t, "")

	_, err = unset.subs.Begin(t.Context(), "antigravity")
	require.ErrorContains(t, err, "HYPOMNEMA_ANTIGRAVITY_CLIENT_SECRET")
	assert.NotContains(t, err.Error(), ideSecret)
}

// Scenario: the owner asks which models Google serves the account. The
// names are the backend's own, in order, with what is left of each quota and
// when it is whole again; a model Google keeps for itself is not listed.
func TestOwnerSeesTheModelsAnAccountIsServed(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	a, err := w.login()
	require.NoError(t, err)

	models, err := w.subs.Models(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, []subscription.ServedModel{
		{Name: "claude-sonnet-5-5", Remaining: 1},
		{Name: "gemini-3.8-flash-high", Remaining: 0.4, ResetsAt: time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)},
		{Name: "gpt-oss-120b-medium", Remaining: -1},
	}, models)
}

// Scenario: two Antigravity accounts' tokens are due together. They are
// refreshed one after the other with twelve seconds between them, not at the
// same moment.
func TestAccountsAreRefreshedOneAfterAnother(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	_, err := w.login()
	require.NoError(t, err)

	w.google.set(func(g *google) { g.user, g.email = "99", "second@gmail.example" })

	_, err = w.login()
	require.NoError(t, err)

	var pauses []time.Duration

	w.subs.RefreshWithoutWaiting(&pauses)
	w.clock.advance(45 * time.Minute)
	require.NoError(t, w.subs.RefreshDue(t.Context()))

	assert.Equal(t, []time.Duration{12 * time.Second}, pauses)

	all, err := w.subs.List(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 2)

	for _, a := range all {
		assert.Equal(t, t0.Add(45*time.Minute+3599*time.Second), *a.AccessTokenExpiresAt, a.Label)
	}
}
