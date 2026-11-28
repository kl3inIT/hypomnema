package signin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
)

var x = signin.Site{
	Name: "X", URL: "https://x.com/i/flow/login", Domains: []string{"x.com", "twitter.com"},
	Login: [][]string{{"auth_token"}, {"ct0"}},
}

// profile is the shared browser's profile: every site's cookies, as Chrome holds them.
type profile struct {
	cookies []browser.Cookie
	opened  []string
}

func (p *profile) OpenOwnerTab(_ context.Context, address string) error {
	p.opened = append(p.opened, address)

	return nil
}

func (p *profile) ProfileCookies(_ context.Context, domain string) ([]browser.Cookie, error) {
	var out []browser.Cookie

	for _, c := range p.cookies {
		d := strings.TrimPrefix(c.Domain, ".")
		if d == domain || strings.HasSuffix(d, "."+domain) {
			out = append(out, c)
		}
	}

	return out, nil
}

func (p *profile) ClearProfileCookies(_ context.Context, cookies []browser.Cookie) error {
	var kept []browser.Cookie

	for _, c := range p.cookies {
		gone := false

		for _, g := range cookies {
			gone = gone || g.Name == c.Name && g.Domain == c.Domain
		}

		if !gone {
			kept = append(kept, c)
		}
	}

	p.cookies = kept

	return nil
}

func TestCaptureTakesTheSitesCookiesOnlyAndClearLeavesTheOthers(t *testing.T) {
	p := &profile{cookies: []browser.Cookie{
		{Name: "auth_token", Value: "a1", Domain: ".x.com"},
		{Name: "ct0", Value: "c1", Domain: ".x.com"},
		{Name: "li_at", Value: "l1", Domain: ".linkedin.com"},
		{Name: "evil", Value: "e1", Domain: ".notx.com"},
	}}

	jar, err := x.Capture(t.Context(), p)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"auth_token", "ct0"}, names(jar))
	assert.Empty(t, x.Missing(jar))

	require.NoError(t, x.Clear(t.Context(), p))
	assert.ElementsMatch(t, []string{"li_at", "evil"}, names(p.cookies), "a sign-out clears that site's cookies only")
}

func TestMissingNamesEachGroupOfLoginCookies(t *testing.T) {
	assert.Equal(t, "auth_token, ct0", x.Missing(nil))
	assert.Equal(t, "ct0", x.Missing([]browser.Cookie{{Name: "auth_token", Value: "a"}, {Name: "ct0"}}),
		"a login cookie without a value is not there")

	douyin := signin.Site{
		Name: "Douyin", Domains: []string{"douyin.com"}, Login: [][]string{{"sessionid", "sessionid_ss"}},
	}
	assert.Empty(t, douyin.Missing([]browser.Cookie{{Name: "sessionid_ss", Value: "s"}}))
	assert.Equal(t, "sessionid or sessionid_ss", douyin.Missing(nil))
}

func TestFromHeaderKeepsAPastedSignInAndNeverEchoesItsValues(t *testing.T) {
	jar, err := x.FromHeader("Cookie: guest_id=v1%3A1; auth_token=secret-token ; ct0=secret-csrf")
	require.NoError(t, err)
	assert.Equal(t, "secret-token", signin.Value(jar, "auth_token"))
	assert.Equal(t, "guest_id=v1%3A1; auth_token=secret-token; ct0=secret-csrf", x.Header(jar, "x.com"))

	_, err = x.FromHeader("guest_id=g; auth_token=secret-token")
	require.ErrorIs(t, err, signin.ErrInvalid)
	assert.Contains(t, err.Error(), "ct0")
	assert.NotContains(t, err.Error(), "secret-token", "an error never carries a cookie's value")

	_, err = x.FromHeader("not a cookie header")
	require.ErrorIs(t, err, signin.ErrInvalid)

	_, err = x.FromHeader("auth_token=a\r\nX-Injected: 1; ct0=c")
	require.ErrorIs(t, err, signin.ErrInvalid, "a value with a line break is dropped, so the login cookie is missing")
}

func TestHeaderSendsTheJarOnlyToTheSitesHosts(t *testing.T) {
	jar := []browser.Cookie{
		{Name: "auth_token", Value: "a", Domain: ".x.com"},
		{Name: "ct0", Value: "c", Domain: ".x.com"},
		{Name: "kdt", Value: "k", Domain: "twitter.com"},
	}

	assert.Equal(t, "auth_token=a; ct0=c", x.Header(jar, "x.com"))
	assert.Equal(t, "auth_token=a; ct0=c", x.Header(jar, "API.X.com:443"))
	assert.Equal(t, "kdt=k", x.Header(jar, "twitter.com"))
	assert.Empty(t, x.Header(jar, "abs.twimg.com"), "a host that is not the site's gets no cookie")
	assert.Empty(t, x.Header(jar, "x.com.evil.example"))
	assert.Empty(t, x.Header(jar, "notx.com"))
}

func TestEncodeDecodeKeepsAJar(t *testing.T) {
	jar := []browser.Cookie{{Name: "ct0", Value: "c", Domain: ".x.com", Path: "/", Secure: true}}

	raw, err := signin.Encode(jar)
	require.NoError(t, err)

	back, err := signin.Decode(raw)
	require.NoError(t, err)
	assert.True(t, signin.Same(jar, back))

	_, err = signin.Decode("{")
	require.Error(t, err)
}

func names(cookies []browser.Cookie) []string {
	out := make([]string, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, c.Name)
	}

	return out
}
