package listening

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Code is why a backend did not read a source: a closed set, part of the tool's answer, so a value is never renamed or
// given a new meaning (linkedin-mcp-server error_handler.py:65-84, "a closed set, and part of the contract"). It is
// Agent-Reach's unified codes (runtime/result.py:22-33) and last30days' run outcomes (lib/health.py:35-44) folded to
// what a listening source can meet, with no payment code: no paid route is used (owner, 2026-11-27).
type Code string

const (
	// CodeNeedLogin: the source answers only a signed-in account (Agent-Reach NEED_LOGIN, last30days auth-failed).
	CodeNeedLogin Code = "need_login"
	// CodeRefused: the source refused this network or account, a 403, a challenge or a block page (Agent-Reach
	// PLATFORM_BLOCKED, linkedin ACCOUNT_RESTRICTED, last30days reddit_health "blocked").
	CodeRefused Code = "refused"
	// CodeRateLimited: the source asked to slow down, a 429; it rests until it says (Agent-Reach RATE_LIMITED).
	CodeRateLimited Code = "rate_limited"
	// CodeLimitReached: a daily quota is spent; it rests until 00:00 UTC (the web search's daily rest, rest.go).
	CodeLimitReached Code = "limit_reached"
	// CodeNotFound: what was asked does not exist, a 404 (Agent-Reach NOT_FOUND).
	CodeNotFound Code = "not_found"
	// CodeTimeout: the source did not answer in time (Agent-Reach TIMEOUT, last30days timeout).
	CodeTimeout Code = "timeout"
	// CodeBroken: the source answered something that is not its answer, or failed another way (Agent-Reach
	// UPSTREAM_BROKEN, last30days schema-drift and error).
	CodeBroken Code = "broken"
	// CodeNoResults: the source was read and had nothing in the window: the only clean zero (last30days
	// no-results, lib/health.py:37).
	CodeNoResults Code = "no_results"
)

// Codes are every code, the most actionable first: the order a source's failures are reported in when several of its
// searches failed differently (last30days FAILURE_SPECIFICITY, lib/health.py:51-59).
func Codes() []Code {
	return []Code{
		CodeNeedLogin, CodeRefused, CodeLimitReached, CodeRateLimited, CodeNotFound, CodeTimeout, CodeBroken,
		CodeNoResults,
	}
}

// specificity is a code's place in Codes: lower is reported first.
func specificity(c Code) int {
	for i, code := range Codes() {
		if code == c {
			return i
		}
	}

	return len(Codes())
}

// SourceError is a backend's failure with its code, what it said, and when the source said to ask again.
type SourceError struct {
	Code   Code
	Detail string
	// Until is when the source said it may be asked again (Retry-After, x-ratelimit-reset); zero when it did not.
	Until time.Time
	// resting: the call was not made, its path resting; the attempt is reported as skipped.
	resting bool
	// pathRest: the failure is a path's, already resting on its own (Request.Rest); the backend is not rested for
	// it, so its other paths serve the next stream.
	pathRest bool
	// transient: a provider behind the backend failed in a way that comes back by itself (a timeout, a rate limit,
	// a spent quota), whatever the code the failure takes; it is kept up to whether the owner is told (health.go).
	transient bool
}

func (f *SourceError) Error() string {
	if f.Detail == "" {
		return string(f.Code)
	}

	return string(f.Code) + ": " + f.Detail
}

// Fail is a failure with a code and what happened.
func Fail(code Code, format string, args ...any) *SourceError {
	return &SourceError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// AsFailure is any backend error as a SourceError: the SourceError it carries, a timeout for a deadline or a network timeout,
// else broken, as Agent-Reach's classify maps any exception to a code (runtime/result.py:68-87).
func AsFailure(err error) *SourceError {
	var f *SourceError
	if errors.As(err, &f) {
		return f
	}

	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return Fail(CodeTimeout, "the source did not answer in time")
	default:
		return Fail(CodeBroken, "%s", oneLine(err.Error()))
	}
}

// HTTPFailure is the failure an HTTP answer's status means: 401 asks for a sign-in, 403 is a refusal, 404 and 410 are
// not found, 429 a rate limit with the wait the answer names, 402 a spent quota, 408 and 504 a timeout, anything else
// broken. From Agent-Reach classify (runtime/result.py:72-77) and last30days classify_failure (lib/http.py:1513-1584),
// with 403 a refusal rather than a sign-in, as last30days' Reddit paths read it (reddit_health.py:10-13).
func HTTPFailure(status int, header http.Header, now time.Time) *SourceError {
	detail := "HTTP " + strconv.Itoa(status)

	switch status {
	case http.StatusUnauthorized:
		return Fail(CodeNeedLogin, "%s", detail)
	case http.StatusForbidden:
		return Fail(CodeRefused, "%s", detail)
	case http.StatusNotFound, http.StatusGone:
		return Fail(CodeNotFound, "%s", detail)
	case http.StatusTooManyRequests:
		f := Fail(CodeRateLimited, "%s", detail)
		if wait, ok := retryAfter(header); ok {
			f.Until = now.Add(wait)
		}

		return f
	case http.StatusPaymentRequired:
		return Fail(CodeLimitReached, "%s", detail)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return Fail(CodeTimeout, "%s", detail)
	default:
		return Fail(CodeBroken, "%s", detail)
	}
}

// retryAfter is the wait a 429 names: Retry-After, else Reddit's x-ratelimit-reset, in seconds (last30days
// retry_delay_from_headers, lib/http.py:77-121; openmagpie parse_rate_limit_wait, connectors/base.py:94-115).
func retryAfter(header http.Header) (time.Duration, bool) {
	for _, name := range []string{"Retry-After", "X-Ratelimit-Reset"} {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(header.Get(name)), 64)
		if err == nil && seconds > 0 && seconds < 7*24*3600 {
			return time.Duration(seconds * float64(time.Second)), true
		}
	}

	return 0, false
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 300 {
		s = string([]rune(s)[:300]) + "…"
	}

	return s
}
