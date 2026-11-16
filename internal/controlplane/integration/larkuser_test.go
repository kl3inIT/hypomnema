package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const ownerName = "The Owner"

// larkAccounts stands in for Lark's sign-in of a person through an app
// (issue #468), as the probe of 2026-11-15 found it: the device
// authorization with the app as Basic, the token endpoint polled with the
// device code, a refresh token that can be used once, the revoke, and the
// user's own information. It checks the app's id and secret on every call.
type larkAccounts struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// pending and slowDowns are how many polls answer authorization_pending, then slow_down, before the
	// owner has entered the code; denied makes the owner refuse instead.
	pending, slowDowns int
	denied             bool
	// noRefresh is an app without "Refresh user_access_token" switched on.
	noRefresh bool
	// deadCode makes every refresh answer that Lark code.
	deadCode int
	// refresh is the one refresh token that works now; access the access tokens Lark gave.
	refresh   string
	issued    int
	refreshes int
	access    map[string]bool
	revoked   []string
	scope     string
	// granted is the scope string a token is issued with; empty is offline_access alone.
	granted string
}

func newLarkAccounts(t *testing.T) *larkAccounts {
	t.Helper()

	l := &larkAccounts{t: t, access: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device", l.device)
	mux.HandleFunc("POST /oauth-token", l.token)
	mux.HandleFunc("POST /revoke", l.revoke)
	mux.HandleFunc("GET /user", l.user)
	l.srv = httptest.NewServer(mux)
	t.Cleanup(l.srv.Close)

	return l
}

// larkUserWorld is a world whose service knows Lark at the stand-ins' addresses, with the owner's app "work"
// connected; paused gets the pause before each poll.
func larkUserWorld(t *testing.T) (*world, *lark, *larkAccounts, *[]time.Duration) {
	t.Helper()

	w, apps, accounts := newWorld(t), newLark(t), newLarkAccounts(t)
	d := apps.definition()
	d.DeviceAuthURL, d.TokenURL = accounts.srv.URL+"/device", accounts.srv.URL+"/oauth-token"
	d.RevokeURL, d.UserInfoURL = accounts.srv.URL+"/revoke", accounts.srv.URL+"/user"

	paused := &[]time.Duration{}
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, d).WithoutPollPauses(paused)

	_, err := w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: appSecret})
	require.NoError(t, err)

	return w, apps, accounts, paused
}

func (l *larkAccounts) device(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(appID+":"+appSecret))
	if r.Header.Get("Authorization") != basic || r.PostFormValue("client_id") != appID {
		l.answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})

		return
	}

	l.scope = r.PostFormValue("scope")
	l.answer(w, http.StatusOK, map[string]any{
		"device_code": "device-1", "user_code": "WXYZ-1234", "verification_uri": "https://accounts.example/device",
		"verification_uri_complete": "https://accounts.example/device?code=WXYZ-1234", "expires_in": 240,
		"interval": 5,
	})
}

func (l *larkAccounts) token(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// A refresh is JSON; the device code is a form.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		l.refreshed(w, r)

		return
	}

	if r.PostFormValue("client_id") != appID || r.PostFormValue("client_secret") != appSecret ||
		r.PostFormValue("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" ||
		r.PostFormValue("device_code") != "device-1" {
		l.answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})

		return
	}

	switch {
	case l.denied:
		l.answer(w, http.StatusBadRequest, map[string]any{"error": "access_denied"})
	case l.pending > 0:
		l.pending--
		l.answer(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
	case l.slowDowns > 0:
		l.slowDowns--
		l.answer(w, http.StatusBadRequest, map[string]any{"error": "slow_down"})
	default:
		l.answer(w, http.StatusOK, l.issue())
	}
}

// issue gives a new pair of tokens: the refresh token before is spent.
func (l *larkAccounts) issue() map[string]any {
	l.issued++
	access := fmt.Sprintf("u-%d", l.issued)
	l.access[access] = true

	scope := "offline_access"
	if l.granted != "" {
		scope = l.granted
	}

	answer := map[string]any{
		"code": 0, "access_token": access, "token_type": "Bearer", "expires_in": 7200, "scope": scope,
	}

	if !l.noRefresh {
		l.refresh = fmt.Sprintf("ur-%d", l.issued)
		answer["refresh_token"], answer["refresh_token_expires_in"] = l.refresh, 604800
	}

	return answer
}

func (l *larkAccounts) refreshed(w http.ResponseWriter, r *http.Request) {
	l.refreshes++

	var asked map[string]string
	if json.NewDecoder(r.Body).Decode(&asked) != nil || asked["client_id"] != appID ||
		asked["client_secret"] != appSecret || asked["grant_type"] != "refresh_token" {
		l.answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})

		return
	}

	if l.deadCode != 0 {
		l.answer(w, http.StatusBadRequest, map[string]any{"code": l.deadCode, "error": "invalid_grant"})

		return
	}

	// A refresh token can only be used once (code 20064, seen on 2026-11-15).
	if asked["refresh_token"] != l.refresh {
		l.answer(w, http.StatusBadRequest, map[string]any{"code": 20064, "error": "invalid_grant"})

		return
	}

	l.answer(w, http.StatusOK, l.issue())
}

func (l *larkAccounts) revoke(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if r.PostFormValue("client_id") != appID || r.PostFormValue("client_secret") != appSecret {
		l.answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})

		return
	}

	l.revoked = append(l.revoked, r.PostFormValue("token_type_hint")+" "+r.PostFormValue("token"))
	l.answer(w, http.StatusOK, map[string]any{})
}

func (l *larkAccounts) user(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()

	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !l.access[token] {
		l.answer(w, http.StatusUnauthorized, map[string]any{"code": 99991668, "msg": "invalid access token"})

		return
	}

	l.answer(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"name": ownerName, "open_id": "ou_1"}})
}

func (l *larkAccounts) answer(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	assert.NoError(l.t, json.NewEncoder(w).Encode(body))
}

func (l *larkAccounts) counts() (issued, refreshes int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.issued, l.refreshes
}

// signInAsOwner is the owner's whole sign-in through the app work: start, enter the code, wait.
func (w *world) signInAsOwner(label string) (integration.Connection, error) {
	w.t.Helper()

	started, err := w.svc.BeginAsUser(w.t.Context(), integration.LarkID, label, "work")
	require.NoError(w.t, err)

	return w.svc.Complete(w.t.Context(), integration.LarkID, started.ID, "")
}

func (w *world) larkConnection(label string) integration.Connection {
	w.t.Helper()

	for _, c := range w.theLark().Connections {
		if c.Label == label {
			return c
		}
	}

	w.t.Fatalf("no lark connection %s", label)

	return integration.Connection{}
}

// Scenario (issue #468, rules 1 to 4): the owner signs in to Lark as himself
// through his app "work", as "me". He is given a link and a code; the
// control plane waits while Lark says he has not entered it yet, and waits
// longer when Lark asks it to slow down. The Connection is his, by his name
// at Lark, and names the app it signed in with; its tokens are sealed in the
// vault. Its token is refreshed five minutes before it ends, and since Lark's
// refresh token can be used once, the new one is kept for the next refresh.
func TestTheOwnerSignsInToLarkAsHimselfThroughHisApp(t *testing.T) {
	t.Parallel()

	w, _, accounts, paused := larkUserWorld(t)
	ctx := t.Context()
	accounts.pending, accounts.slowDowns = 1, 1

	started, err := w.svc.BeginAsUser(ctx, integration.LarkID, "me", "work")
	require.NoError(t, err)
	assert.Equal(t, "WXYZ-1234", started.UserCode)
	assert.Equal(t, "https://accounts.example/device?code=WXYZ-1234", started.URL)
	assert.Equal(t, 4*time.Minute, started.ExpiresIn)
	assert.Contains(t, accounts.scope, "offline_access", "a refresh token is always asked for")

	_, err = w.svc.Complete(ctx, integration.LarkID, started.ID, "http://localhost:1/?code=x")
	require.ErrorIs(t, err, integration.ErrInvalid, "a sign-in by code takes no address")

	conn, err := w.svc.Complete(ctx, integration.LarkID, started.ID, "")
	require.NoError(t, err)
	assert.Equal(t, ownerName, conn.Account)
	assert.Equal(t, "work", conn.AppLabel)
	assert.Equal(t, appID, conn.AppID)
	assert.False(t, conn.SignInRequired)
	assert.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}, *paused,
		"Lark's interval, and five seconds more after slow_down")

	_, err = w.svc.Complete(ctx, integration.LarkID, started.ID, "")
	require.ErrorIs(t, err, integration.ErrNotFound, "a sign-in is completed once")

	stored := w.stored()
	assert.NotContains(t, stored, "ur-1", "the refresh token is sealed")
	assert.NotContains(t, stored, "u-1")

	token, err := w.svc.Token(ctx, integration.LarkID, "me")
	require.NoError(t, err)
	assert.Equal(t, "u-1", token.AccessToken)
	assert.Equal(t, appID, token.ClientID, "Lark's tool needs the app's id beside a person's token too")

	w.clock.advance(2*time.Hour - 6*time.Minute)

	token, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.NoError(t, err)
	assert.Equal(t, "u-1", token.AccessToken, "six minutes left: still fresh")

	w.clock.advance(2 * time.Minute)

	token, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.NoError(t, err)
	assert.Equal(t, "u-2", token.AccessToken, "four minutes left: refreshed first")

	w.clock.advance(2 * time.Hour)

	token, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.NoError(t, err, "the second refresh uses the refresh token the first one gave")
	assert.Equal(t, "u-3", token.AccessToken)

	issued, refreshes := accounts.counts()
	assert.Equal(t, 3, issued)
	assert.Equal(t, 2, refreshes)

	app := w.larkConnection("work")
	assert.Empty(t, app.AppLabel, "the app stays an app")
}

// Scenario (issue #468, rule 3): an app without "Refresh user_access_token"
// switched on gives no refresh token. The sign-in is refused with what to
// change in Lark's console, and nothing is kept. One the owner refused on
// Lark's page is refused too.
func TestALarkSignInThatWouldNotLastIsRefused(t *testing.T) {
	t.Parallel()

	w, _, accounts, _ := larkUserWorld(t)
	accounts.noRefresh = true

	_, err := w.signInAsOwner("me")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Contains(t, err.Error(), `"Refresh user_access_token"`)
	assert.Contains(t, err.Error(), "https://open.larksuite.com/app/"+appID+"/safe")
	assert.Len(t, w.theLark().Connections, 1, "only the app is connected")

	accounts.noRefresh, accounts.denied = false, true

	_, err = w.signInAsOwner("me")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Contains(t, err.Error(), "refused on Lark's page")
}

// Scenario (issue #468, rule 4): Lark answers a refresh with a code that ends
// the sign-in for good (here 20037, the refresh token expired after its seven
// days). The Connection needs a sign-in, says so in the list, and signing in
// again under the same label makes it work.
func TestLarkEndingTheOwnersSignInMarksItNeedingOne(t *testing.T) {
	t.Parallel()

	w, _, accounts, _ := larkUserWorld(t)
	ctx := t.Context()

	_, err := w.signInAsOwner("me")
	require.NoError(t, err)

	accounts.deadCode = 20037

	w.clock.advance(2 * time.Hour)

	_, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.True(t, w.larkConnection("me").SignInRequired)

	_, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.ErrorIs(t, err, integration.ErrSignInRequired, "a dead sign-in is not tried again")

	_, refreshes := accounts.counts()
	assert.Equal(t, 1, refreshes)

	accounts.deadCode = 0

	_, err = w.signInAsOwner("me")
	require.NoError(t, err)
	assert.False(t, w.larkConnection("me").SignInRequired)

	_, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.NoError(t, err)
}

// Scenario (issue #468, rules 1 and 9): the app "work" cannot be disconnected
// while the owner's sign-in uses it. Disconnecting "me" asks Lark to revoke
// both its tokens, with the app's id and secret, and forgets it; then the app
// can go. A label that is an app is not signed in as the owner, nor is an app
// given to his sign-in's label, nor is a sign-in made through an app that is
// not there.
func TestTheOwnersLarkSignInIsRevokedAndHoldsItsApp(t *testing.T) {
	t.Parallel()

	w, _, accounts, _ := larkUserWorld(t)
	ctx := t.Context()

	_, err := w.signInAsOwner("me")
	require.NoError(t, err)

	_, err = w.svc.BeginAsUser(ctx, integration.LarkID, "work", "work")
	require.ErrorIs(t, err, integration.ErrInvalid, "work is an app")

	_, err = w.svc.BeginAsUser(ctx, integration.LarkID, "me", "nothing")
	require.ErrorIs(t, err, integration.ErrInvalid)

	_, err = w.svc.Connect(ctx, integration.LarkID, "me", integration.App{ID: reportID, Secret: reportSecret})
	require.ErrorIs(t, err, integration.ErrInvalid, "the owner's sign-in is not turned into an app")

	_, err = w.svc.Disconnect(ctx, integration.LarkID, "work")
	require.ErrorIs(t, err, orchestration.ErrConflict)
	assert.Contains(t, err.Error(), "lark:me")

	revoked, err := w.svc.Disconnect(ctx, integration.LarkID, "me")
	require.NoError(t, err)
	assert.True(t, revoked)
	assert.Equal(t, []string{"refresh_token ur-1", "access_token u-1"}, accounts.revoked)

	_, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.ErrorIs(t, err, integration.ErrNotFound)

	_, err = w.svc.Disconnect(ctx, integration.LarkID, "work")
	require.NoError(t, err)
}

// Scenario (issue #468): the owner gives "work" another app. A refresh token
// is of the app that issued it, so his sign-in through the old one needs a
// sign-in again, and no refresh is sent with the new app's secret.
func TestChangingTheAppOfTheOwnersSignInNeedsASignInAgain(t *testing.T) {
	t.Parallel()

	w, _, accounts, _ := larkUserWorld(t)
	ctx := t.Context()

	_, err := w.signInAsOwner("me")
	require.NoError(t, err)

	_, err = w.svc.Connect(ctx, integration.LarkID, "work", integration.App{ID: reportID, Secret: reportSecret})
	require.NoError(t, err)
	assert.True(t, w.larkConnection("me").SignInRequired)

	w.clock.advance(2 * time.Hour)

	_, err = w.svc.Token(ctx, integration.LarkID, "me")
	require.ErrorIs(t, err, integration.ErrSignInRequired)

	_, refreshes := accounts.counts()
	assert.Zero(t, refreshes)
}
