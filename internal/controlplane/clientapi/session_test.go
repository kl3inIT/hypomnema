package clientapi

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

const (
	userName     = "admin"
	userPassword = "correct horse"
)

// browser is an HTTP client that keeps cookies, as a browser does.
type browser struct {
	t    *testing.T
	base string
	http *http.Client
}

func (w *world) browser() *browser {
	w.t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(w.t, err)

	return &browser{t: w.t, base: w.url + BasePath, http: &http.Client{Jar: jar}}
}

func (b *browser) do(method, path, body string) *http.Response {
	b.t.Helper()

	req, err := http.NewRequestWithContext(b.t.Context(), method, b.base+path, strings.NewReader(body))
	require.NoError(b.t, err)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.http.Do(req)
	require.NoError(b.t, err)
	b.t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

func (b *browser) signIn(name, password string) *http.Response {
	return b.do(http.MethodPost, "/session", `{"username":"`+name+`","password":"`+password+`"}`)
}

// Scenario: the owner signs in to the web with the user made on the server.
// The browser gets a cookie that scripts cannot read and that it sends on the
// next requests, where it acts as the owner. Signing out ends the session:
// the same cookie, kept by someone, opens nothing. A token still works
// beside it.
func TestAUserSignsInAndOut(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, err := w.svc.AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	b := w.browser()
	assert.Equal(t, http.StatusUnauthorized, b.do(http.MethodGet, "/me", "").StatusCode)

	resp := b.signIn(userName, userPassword)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "hypomnema_session", cookies[0].Name)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	assert.Equal(t, "/", cookies[0].Path)
	assert.False(t, cookies[0].Secure, "the test server is plain HTTP")
	assert.InDelta(t, 30*24*time.Hour, time.Until(cookies[0].Expires), float64(time.Minute))

	me, err := openapi.ParseGetMeResponse(b.do(http.MethodGet, "/me", ""))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, me.StatusCode(), string(me.Body))
	assert.Equal(t, userName, me.JSON200.Name)
	assert.Equal(t, openapi.TokenKind(bearertoken.Owner), me.JSON200.Kind)

	// Only the owner may add a node: the session is the owner.
	assert.Equal(t, http.StatusCreated, b.do(http.MethodPost, "/nodes", `{"name":"zm"}`).StatusCode)

	kept := cookies[0]

	out := b.do(http.MethodDelete, "/session", "")
	require.Equal(t, http.StatusNoContent, out.StatusCode)
	require.Len(t, out.Cookies(), 1)
	assert.Negative(t, out.Cookies()[0].MaxAge, "the browser is told to drop the cookie")
	assert.Equal(t, http.StatusUnauthorized, b.do(http.MethodGet, "/me", "").StatusCode)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, b.base+"/me", nil)
	require.NoError(t, err)
	req.AddCookie(kept)

	again, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, again.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, again.StatusCode, "a signed-out session is gone on the server")

	c, tok := w.token("laptop", bearertoken.Owner)
	viaToken, err := c.GetMeWithResponse(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, viaToken.StatusCode())
	assert.Equal(t, tok.Name, viaToken.JSON200.Name)
}

// Scenario: behind HTTPS (the proxy says so) the cookie is marked Secure and
// carries the __Host- prefix, so no other host or plain connection can set it.
func TestTheCookieIsSecureBehindHTTPS(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, err := w.svc.AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.url+BasePath+"/session",
		strings.NewReader(`{"username":"`+userName+`","password":"`+userPassword+`"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Len(t, resp.Cookies(), 1)
	assert.Equal(t, "__Host-hypomnema_session", resp.Cookies()[0].Name)
	assert.True(t, resp.Cookies()[0].Secure)
}

// Scenario: a new password, set on the server. Every browser signed in with
// the old one is signed out; the old password no longer signs in.
func TestANewPasswordEndsEverySession(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, err := w.svc.AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	laptop, phone := w.browser(), w.browser()
	require.Equal(t, http.StatusNoContent, laptop.signIn(userName, userPassword).StatusCode)
	require.Equal(t, http.StatusNoContent, phone.signIn(userName, userPassword).StatusCode)

	require.NoError(t, w.svc.SetUserPassword(t.Context(), userName, "battery staple"))

	assert.Equal(t, http.StatusUnauthorized, laptop.do(http.MethodGet, "/me", "").StatusCode)
	assert.Equal(t, http.StatusUnauthorized, phone.do(http.MethodGet, "/me", "").StatusCode)
	assert.Equal(t, http.StatusUnauthorized, laptop.signIn(userName, userPassword).StatusCode)
	assert.Equal(t, http.StatusNoContent, laptop.signIn(userName, "battery staple").StatusCode)
}

// Scenario: someone guesses passwords. A wrong password and an unknown name
// get the same answer. The fifth wrong try locks the address for 30 seconds,
// during which even the right password is refused with the time to wait;
// after the wait the right password signs in. Five more wrong tries lock it
// for two minutes.
func TestWrongPasswordsLockTheAddress(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, err := w.svc.AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	var mu sync.Mutex

	now := time.Now()
	w.srv.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()

		now = now.Add(d)
	}

	b := w.browser()

	wrong := b.signIn(userName, "not the password")
	nobody := b.signIn("nobody", "not the password")
	assert.Equal(t, http.StatusUnauthorized, wrong.StatusCode)
	assert.Equal(t, http.StatusUnauthorized, nobody.StatusCode)

	for range 2 {
		assert.Equal(t, http.StatusUnauthorized, b.signIn(userName, "not the password").StatusCode)
	}

	fifth := b.signIn(userName, "not the password")
	assert.Equal(t, http.StatusTooManyRequests, fifth.StatusCode)
	assert.Equal(t, "30", fifth.Header.Get("Retry-After"))

	advance(10 * time.Second)

	locked := b.signIn(userName, userPassword)
	assert.Equal(t, http.StatusTooManyRequests, locked.StatusCode)
	assert.Equal(t, "20", locked.Header.Get("Retry-After"))

	advance(21 * time.Second)

	for range 4 {
		assert.Equal(t, http.StatusUnauthorized, b.signIn(userName, "not the password").StatusCode)
	}

	again := b.signIn(userName, "not the password")
	assert.Equal(t, http.StatusTooManyRequests, again.StatusCode)
	assert.Equal(t, "120", again.Header.Get("Retry-After"), "the second lock is longer")

	advance(121 * time.Second)
	assert.Equal(t, http.StatusNoContent, b.signIn(userName, userPassword).StatusCode)
}
