package clientapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strconv"
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

// reply is what came back, read to its end.
type reply struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
	body    []byte
}

func (b *browser) do(method, path, body string) reply {
	b.t.Helper()

	req, err := http.NewRequestWithContext(b.t.Context(), method, b.base+path, strings.NewReader(body))
	require.NoError(b.t, err)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.http.Do(req)
	require.NoError(b.t, err)

	defer func() { _ = resp.Body.Close() }()

	read, err := io.ReadAll(resp.Body)
	require.NoError(b.t, err)

	return reply{status: resp.StatusCode, header: resp.Header, cookies: resp.Cookies(), body: read}
}

func (b *browser) signIn(name, password string) reply {
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
	_, err := w.svc.Users().AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	b := w.browser()
	assert.Equal(t, http.StatusUnauthorized, b.do(http.MethodGet, "/me", "").status)

	resp := b.signIn(userName, userPassword)
	require.Equal(t, http.StatusNoContent, resp.status)

	cookies := resp.cookies
	require.Len(t, cookies, 1)
	assert.Equal(t, "hypomnema_session", cookies[0].Name)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	assert.Equal(t, "/", cookies[0].Path)
	assert.False(t, cookies[0].Secure, "the test server is plain HTTP")
	assert.InDelta(t, 30*24*time.Hour, time.Until(cookies[0].Expires), float64(time.Minute))

	got := b.do(http.MethodGet, "/me", "")
	require.Equal(t, http.StatusOK, got.status, string(got.body))

	var me openapi.Me
	require.NoError(t, json.Unmarshal(got.body, &me))
	assert.Equal(t, userName, me.Name)
	assert.Equal(t, openapi.TokenKind(bearertoken.Owner), me.Kind)

	// Only the owner may add a node: the session is the owner.
	assert.Equal(t, http.StatusCreated, b.do(http.MethodPost, "/nodes", `{"name":"zm"}`).status)

	kept := cookies[0]

	out := b.do(http.MethodDelete, "/session", "")
	require.Equal(t, http.StatusNoContent, out.status)
	require.Len(t, out.cookies, 1)
	assert.Negative(t, out.cookies[0].MaxAge, "the browser is told to drop the cookie")
	assert.Equal(t, http.StatusUnauthorized, b.do(http.MethodGet, "/me", "").status)

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
	_, err := w.svc.Users().AddUser(t.Context(), userName, userPassword)
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
	_, err := w.svc.Users().AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	laptop, phone := w.browser(), w.browser()
	require.Equal(t, http.StatusNoContent, laptop.signIn(userName, userPassword).status)
	require.Equal(t, http.StatusNoContent, phone.signIn(userName, userPassword).status)

	require.NoError(t, w.svc.Users().SetUserPassword(t.Context(), userName, "battery staple"))

	assert.Equal(t, http.StatusUnauthorized, laptop.do(http.MethodGet, "/me", "").status)
	assert.Equal(t, http.StatusUnauthorized, phone.do(http.MethodGet, "/me", "").status)
	assert.Equal(t, http.StatusUnauthorized, laptop.signIn(userName, userPassword).status)
	assert.Equal(t, http.StatusNoContent, laptop.signIn(userName, "battery staple").status)
}

// Scenario: someone guesses passwords. A wrong password and an unknown name
// get the same answer. The fifth wrong try locks the address for 30 seconds,
// during which even the right password is refused with the time to wait;
// after the wait the right password signs in. Five more wrong tries lock it
// for two minutes.
func TestWrongPasswordsLockTheAddress(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, err := w.svc.Users().AddUser(t.Context(), userName, userPassword)
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
	assert.Equal(t, http.StatusUnauthorized, wrong.status)
	assert.Equal(t, http.StatusUnauthorized, nobody.status)

	for range 2 {
		assert.Equal(t, http.StatusUnauthorized, b.signIn(userName, "not the password").status)
	}

	fifth := b.signIn(userName, "not the password")
	assert.Equal(t, http.StatusTooManyRequests, fifth.status)
	assert.Equal(t, "30", fifth.header.Get("Retry-After"))

	advance(10 * time.Second)

	locked := b.signIn(userName, userPassword)
	assert.Equal(t, http.StatusTooManyRequests, locked.status)
	assert.Equal(t, "20", locked.header.Get("Retry-After"))

	advance(21 * time.Second)

	for range 4 {
		assert.Equal(t, http.StatusUnauthorized, b.signIn(userName, "not the password").status)
	}

	second := b.signIn(userName, "not the password")
	assert.Equal(t, http.StatusTooManyRequests, second.status)
	assert.Equal(t, "120", second.header.Get("Retry-After"), "the second lock is longer")

	advance(121 * time.Second)
	assert.Equal(t, http.StatusNoContent, b.signIn(userName, userPassword).status)
}

// Scenario: someone guessing passwords through the proxy sends a different
// made-up address with each try. The proxy appends the real one after it, and
// that is the one counted: the fifth try is locked all the same.
func TestAMadeUpForwardedAddressDoesNotEscapeTheLock(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	_, err := w.svc.Users().AddUser(t.Context(), userName, userPassword)
	require.NoError(t, err)

	try := func(madeUp string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.url+BasePath+"/session",
			strings.NewReader(`{"username":"`+userName+`","password":"not the password"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", madeUp+", 100.64.0.7")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())

		return resp.StatusCode
	}

	for i := range 4 {
		assert.Equal(t, http.StatusUnauthorized, try("203.0.113."+strconv.Itoa(i+1)))
	}

	assert.Equal(t, http.StatusTooManyRequests, try("203.0.113.99"))
}
