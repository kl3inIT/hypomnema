package websearch

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A provider that answered 429 rests, as the gateway's accounts cool down
// after a rate limit: for the Retry-After it named, else 2 s doubling with
// each 429 in a row up to 5 minutes (9router open-sse/config/errorConfig.js,
// BACKOFF_CONFIG; services/accountFallback.js, getQuotaCooldown). Only the
// search of that provider rests: nothing else uses it. The count is in
// memory, and an answer clears it.
const (
	restFirst  = 2 * time.Second
	restMax    = 5 * time.Minute
	restLevels = 15
	// longestRetryAfter bounds a Retry-After that is believed (9router's
	// MAX_RATE_LIMIT_COOLDOWN_MS).
	longestRetryAfter = 30 * time.Minute
)

type rest struct {
	until time.Time
	level int
}

// providerError is a provider's refusal: its status, and the wait it asked for.
type providerError struct {
	status     int
	retryAfter time.Duration
	message    string
}

func (e *providerError) Error() string {
	if e.status == http.StatusTooManyRequests {
		return "rate limited (429)"
	}

	if e.message == "" {
		return fmt.Sprintf("answered %d", e.status)
	}

	return fmt.Sprintf("answered %d: %s", e.status, e.message)
}

// refusal reads a provider's answer that is not a success.
func refusal(resp *http.Response, body []byte, now time.Time) *providerError {
	return &providerError{
		status: resp.StatusCode, retryAfter: retryAfter(resp.Header.Get("Retry-After"), now),
		message: clip(strings.TrimSpace(string(body)), 200),
	}
}

// retryAfter reads a Retry-After header: seconds, or an HTTP date.
func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return min(time.Duration(seconds)*time.Second, longestRetryAfter)
	}

	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), longestRetryAfter)
	}

	return 0
}

// reasonOf is an error as a failure's reason.
func reasonOf(err error) string {
	return clip(err.Error(), 300)
}

func (s *Service) restingUntil(id string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.resting[id]
	if !ok || !r.until.After(s.now()) {
		return time.Time{}, false
	}

	return r.until, true
}

// failed makes a route rest when the provider answered 429.
func (s *Service) failed(id string, err error) {
	var refused *providerError
	if !errors.As(err, &refused) || refused.status != http.StatusTooManyRequests {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r := s.resting[id]
	r.level = min(r.level+1, restLevels)

	wait := refused.retryAfter
	if wait <= 0 {
		wait = min(restFirst<<(r.level-1), restMax)
	}

	r.until = s.now().Add(wait)
	s.resting[id] = r
}

// served forgets a route's 429s in a row.
func (s *Service) served(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.resting, id)
}

// clip cuts a text at n characters.
func clip(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}

	return string(runes[:n])
}
