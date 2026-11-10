package clientapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The web's session (issue #114). Each rule names the reference it is learned
// from (docs/research/2026-11-10-web-login.md):
//
//   - the browser holds an HttpOnly cookie, SameSite=Lax, Path=/, with Secure
//     and the __Host- prefix on HTTPS: Omnigent;
//   - HTTPS is the connection's own TLS or X-Forwarded-Proto: 9router-go;
//   - a request is authenticated by its cookie first, then by its bearer
//     token: Omnigent;
//   - after 5 wrong tries from one address within an hour the address is
//     locked for 30 seconds, then 2, 10 and 30 minutes, answered 429 with
//     Retry-After; this is kept in memory for at most 5000 addresses: 9router
//     and 9router-go.

const (
	sessionCookie       = "hypomnema_session"
	secureSessionCookie = "__Host-" + sessionCookie

	loginTries   = 5
	loginWindow  = time.Hour
	loginBuckets = 5000
)

var loginLocks = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// lockedError says an address must wait before it tries a password again.
type lockedError struct{ wait time.Duration }

func (e *lockedError) Error() string {
	return fmt.Sprintf("too many wrong tries; try again in %s", e.wait.Round(time.Second))
}

// caller is what a handler needs to know of the request beyond its body.
type caller struct {
	// secure says the browser reached the server over HTTPS.
	secure bool
	// address is where the request came from.
	address string
	// sessionID is the web session the request was authenticated by, if any.
	sessionID string
}

type callerKey struct{}

func callerOf(ctx context.Context) caller {
	c, _ := ctx.Value(callerKey{}).(caller)

	return c
}

func newCaller(r *http.Request) caller {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// A proxy on this machine (tailscale serve) names the browser's address.
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(first) != "" {
			host = strings.TrimSpace(first)
		}
	}

	return caller{
		secure:  r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		address: host,
	}
}

// sessionSecret is the secret of the request's session cookie, "" without one.
func sessionSecret(r *http.Request) string {
	for _, name := range []string{secureSessionCookie, sessionCookie} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}

	return ""
}

func cookieFor(c caller, secret string, expires time.Time) *string {
	cookie := http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: c.secure, Expires: expires,
	}

	if c.secure {
		cookie.Name = secureSessionCookie
	}

	if secret == "" {
		cookie.MaxAge = -1
	}

	out := cookie.String()

	return &out
}

// loginLimiter counts wrong tries per address.
type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginBucket
}

type loginBucket struct {
	failures int
	first    time.Time
	// level is how many times the address was locked.
	level       int
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{buckets: map[string]*loginBucket{}} }

// locked returns how long an address must still wait.
func (l *loginLimiter) locked(address string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if b, ok := l.buckets[address]; ok && now.Before(b.lockedUntil) {
		return b.lockedUntil.Sub(now)
	}

	return 0
}

// failed counts a wrong try and returns the lock it brings, if any.
func (l *loginLimiter) failed(address string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[address]
	if !ok {
		if len(l.buckets) >= loginBuckets {
			l.prune(now)
		}

		b = &loginBucket{}
		l.buckets[address] = b
	}

	if b.failures == 0 || now.Sub(b.first) > loginWindow {
		b.failures, b.first = 0, now
	}

	b.failures++
	if b.failures < loginTries {
		return 0
	}

	wait := loginLocks[min(b.level, len(loginLocks)-1)]
	b.level++
	b.failures = 0
	b.lockedUntil = now.Add(wait)

	return wait
}

// succeeded forgets an address's wrong tries.
func (l *loginLimiter) succeeded(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.buckets, address)
}

// prune drops the addresses that are neither locked nor inside their window.
func (l *loginLimiter) prune(now time.Time) {
	for address, b := range l.buckets {
		if now.After(b.lockedUntil) && now.Sub(b.first) > loginWindow {
			delete(l.buckets, address)
		}
	}
}

// CreateSession signs a user in.
func (s *Server) CreateSession(
	ctx context.Context, req openapi.CreateSessionRequestObject,
) (openapi.CreateSessionResponseObject, error) {
	c, now := callerOf(ctx), s.now()

	if wait := s.logins.locked(c.address, now); wait > 0 {
		return nil, &lockedError{wait: wait}
	}

	session, err := s.svc.SignIn(ctx, req.Body.Username, req.Body.Password)
	if errors.Is(err, orchestration.ErrBadCredentials) {
		if wait := s.logins.failed(c.address, now); wait > 0 {
			return nil, &lockedError{wait: wait}
		}

		return nil, fmt.Errorf("%w: %w", errUnauthorized, err)
	}

	if err != nil {
		return nil, err
	}

	s.logins.succeeded(c.address)

	return openapi.CreateSession204Response{Headers: openapi.CreateSession204ResponseHeaders{
		SetCookie: cookieFor(c, session.Secret, session.ExpiresAt),
	}}, nil
}

// DeleteSession signs the request's session out. A request made with a
// token has no session, and only loses a cookie it may carry.
func (s *Server) DeleteSession(
	ctx context.Context, _ openapi.DeleteSessionRequestObject,
) (openapi.DeleteSessionResponseObject, error) {
	c := callerOf(ctx)

	if c.sessionID != "" {
		if err := s.svc.SignOut(ctx, c.sessionID); err != nil {
			return nil, err
		}
	}

	return openapi.DeleteSession204Response{Headers: openapi.DeleteSession204ResponseHeaders{
		SetCookie: cookieFor(c, "", time.Unix(0, 0)),
	}}, nil
}

// retryAfter is a wait as whole seconds, at least one.
func retryAfter(wait time.Duration) string {
	return strconv.Itoa(max(1, int(wait.Round(time.Second).Seconds())))
}
