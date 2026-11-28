// Package signin is how the control plane keeps a signed-in account of a site (issue #1291), generalized from the
// Muse pool's sign-in (issue #782, muse/signin.go) so that Muse and the signed-in listening sources use one way:
//
//   - the owner signs in himself, in his own tab of the shared browser (Site.Open, the browser's OpenOwnerTab); the
//     agent never fills the form (last30days box_chrome_login.py and setup-wizard.md: "Do NOT fill the login form
//     … hand the desktop … to the human"; linkedin-mcp-server's --login-viewer; Agent-Reach sessions.md:27, "the
//     user completes the login himself; you do not log in for him, ask for no password and print no cookie");
//   - the site's cookies are then read from the shared profile, only that site's (Site.Capture; Agent-Reach
//     sessions.md:17 copies "only that site's cookies"), checked for the site's login cookies (Site.Missing,
//     last30days-skill-cn crawler_bridge.py:77-112 LOGIN_SPECS and has_login :320-331), kept in the vault as the
//     account's jar (Encode) and cleared from the shared profile (Site.Clear), so that no agent's tab carries the
//     session and the account is asked in a browser context of its own (browser/contexts.go);
//   - when the browser cannot be used, a cookie header copied from a signed-in browser stands in for it, checked for
//     the same login cookies (Site.FromHeader, crawler_bridge.py:378-410 import_cookie_header);
//   - cookies present do not prove a session: the caller verifies it by a real action before keeping it (Agent-Reach
//     sessions.md:18, "the cookie being there does not mean the login is valid; the result of a real action
//     decides"; last30days reddit_session.py);
//   - a cookie goes only to its own site (Site.Header; last30days CONFIGURATION.md:372, "sent only in a Cookie header
//     to https://www.reddit.com … a redirect anywhere else drops it"), and its value is never printed nor logged:
//     nothing here formats one into an error or a log line (last30days bird_x.py:210-237 scrubs them from output).
package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
)

// ErrInvalid reports a pasted cookie header that cannot be a sign-in; its message names what is missing, never a
// value.
var ErrInvalid = errors.New("invalid")

// Site is a site whose account is signed in: where the owner signs in, the domains its cookies are set on, and the
// cookies only a signed-in session has.
type Site struct {
	// Name names it in errors.
	Name string
	// URL is the page opened in the owner's tab to sign in.
	URL string
	// Domains are the site's own: a cookie set on one of them, or under one, is the site's.
	Domains []string
	// Login are the cookies a signed-in session has: every group must have one of its names with a value (X needs
	// both auth_token and ct0; Douyin either sessionid or sessionid_ss: crawler_bridge.py:77-112).
	Login [][]string
}

// Profile is what a sign-in uses of the shared browser.
type Profile interface {
	OpenOwnerTab(ctx context.Context, address string) error
	ProfileCookies(ctx context.Context, domain string) ([]browser.Cookie, error)
	ClearProfileCookies(ctx context.Context, cookies []browser.Cookie) error
}

// Open loads the site's sign-in page in the owner's tab.
func (s Site) Open(ctx context.Context, p Profile) error {
	return p.OpenOwnerTab(ctx, s.URL)
}

// Owns says a cookie's domain, or a request's host, is the site's.
func (s Site) Owns(domain string) bool {
	domain = strings.ToLower(strings.TrimPrefix(domain, "."))

	return slices.ContainsFunc(s.Domains, func(d string) bool { return domain == d || strings.HasSuffix(domain, "."+d) })
}

// Cookies are the site's among cookies.
func (s Site) Cookies(cookies []browser.Cookie) []browser.Cookie {
	var out []browser.Cookie

	for _, c := range cookies {
		if s.Owns(c.Domain) {
			out = append(out, c)
		}
	}

	return out
}

// Missing names the login cookies a jar lacks, each group's names joined by "or"; empty when it has them.
func (s Site) Missing(jar []browser.Cookie) string {
	var missing []string

	for _, group := range s.Login {
		has := slices.ContainsFunc(group, func(name string) bool {
			return slices.ContainsFunc(jar, func(c browser.Cookie) bool { return c.Name == name && c.Value != "" })
		})
		if !has {
			missing = append(missing, strings.Join(group, " or "))
		}
	}

	return strings.Join(missing, ", ")
}

// Capture reads the site's cookies the shared profile holds, the owner's sign-in in his tab having set them.
func (s Site) Capture(ctx context.Context, p Profile) ([]browser.Cookie, error) {
	var out []browser.Cookie

	for _, d := range s.Domains {
		got, err := p.ProfileCookies(ctx, d)
		if err != nil {
			return nil, err
		}

		for _, c := range s.Cookies(got) {
			if !slices.ContainsFunc(out, func(o browser.Cookie) bool { return sameCookie(o, c) }) {
				out = append(out, c)
			}
		}
	}

	return out, nil
}

// Clear removes the site's cookies from the shared profile and leaves every other site's: a sign-out clears that
// site's cookies only (Agent-Reach sessions.md:39-41, logout and remove).
func (s Site) Clear(ctx context.Context, p Profile) error {
	cookies, err := s.Capture(ctx, p)
	if err != nil || len(cookies) == 0 {
		return err
	}

	return p.ClearProfileCookies(ctx, cookies)
}

// FromHeader is a cookie header copied from a signed-in browser ("a=1; b=2") as the site's jar, one cookie on each of
// its domains, checked for its login cookies (crawler_bridge.py:378-410). Its error names the missing cookies, never
// a value.
func (s Site) FromHeader(header string) ([]browser.Cookie, error) {
	header = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "Cookie:"))

	var jar []browser.Cookie

	for part := range strings.SplitSeq(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)

		if !ok || name == "" || !validCookie(name, value) {
			continue
		}

		for _, d := range s.Domains {
			jar = append(jar, browser.Cookie{Name: name, Value: value, Domain: "." + d, Path: "/", Secure: true})
		}
	}

	if len(jar) == 0 {
		return nil, fmt.Errorf("%w: no cookie in what was pasted; copy the Cookie header of a request to %s",
			ErrInvalid, s.Domains[0])
	}

	if missing := s.Missing(jar); missing != "" {
		return nil, fmt.Errorf("%w: %s's sign-in cookies are missing (%s); copy the whole Cookie header from a "+
			"browser signed in to %s", ErrInvalid, s.Name, missing, s.Domains[0])
	}

	return jar, nil
}

// validCookie says a cookie may go in a header: no control character, no separator in its name, none of ";" or ","
// in its value (RFC 6265 §4.1.1), as bird checks its cookies before it sends them (vendor/bird-search/lib/cookies.js:13-18).
func validCookie(name, value string) bool {
	for _, r := range name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return false
		}
	}

	for _, r := range value {
		if r < ' ' || r >= 0x7f || r == ';' || r == ',' || r == '\\' {
			return false
		}
	}

	return true
}

// Header is the Cookie header of a request to host: the jar's cookies for that host. A host that is not the site's
// gets none: a cookie goes only to its own site.
func (s Site) Header(jar []browser.Cookie, host string) string {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}

	if !s.Owns(host) {
		return ""
	}

	var parts []string

	for _, c := range jar {
		d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		if (host == d || strings.HasSuffix(host, "."+d)) &&
			!slices.ContainsFunc(parts, func(p string) bool { return strings.HasPrefix(p, c.Name+"=") }) {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}

	return strings.Join(parts, "; ")
}

// Value is a cookie's value in a jar, empty when it has none.
func Value(jar []browser.Cookie, name string) string {
	for _, c := range jar {
		if c.Name == name {
			return c.Value
		}
	}

	return ""
}

// Encode is a jar as the vault keeps it.
func Encode(jar []browser.Cookie) (string, error) {
	raw, err := json.Marshal(jar)
	if err != nil {
		return "", fmt.Errorf("encoding a cookie jar: %w", err)
	}

	return string(raw), nil
}

// Decode is a jar the vault kept.
func Decode(raw string) ([]browser.Cookie, error) {
	var jar []browser.Cookie
	if err := json.Unmarshal([]byte(raw), &jar); err != nil {
		return nil, errors.New("the kept cookies do not decode")
	}

	return jar, nil
}

// Same says two jars hold the same cookies, whatever their order.
func Same(a, b []browser.Cookie) bool {
	key := func(cs []browser.Cookie) []string {
		out := make([]string, 0, len(cs))
		for _, c := range cs {
			out = append(out, c.Domain+"\x00"+c.Path+"\x00"+c.Name+"\x00"+c.Value)
		}

		slices.Sort(out)

		return out
	}

	return slices.Equal(key(a), key(b))
}

func sameCookie(a, b browser.Cookie) bool {
	return a.Domain == b.Domain && a.Path == b.Path && a.Name == b.Name
}
