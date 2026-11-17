package speech

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A key that a provider turned away rests, and is skipped meanwhile; each key
// rests on its own, as web search's keys do (websearch/rest.go, issue #393),
// for as long as there:
//
//   - rate limited (429): for the Retry-After it named, else 2 s doubling
//     with each 429 in a row up to 5 minutes (9router errorConfig.js,
//     BACKOFF_CONFIG);
//   - out of credits (402, or an answer whose words say the credits are
//     spent, web-search-plus's _out_of_credit): 5 minutes, doubling with each
//     in a row up to an hour;
//   - refused (401, 403): 2 minutes (9router's COOLDOWN.long).
//
// The rests are in memory, and an answer clears them.
const (
	restFirst  = 2 * time.Second
	restMax    = 5 * time.Minute
	restLevels = 15
	// longestRetryAfter bounds a Retry-After that is believed (9router's MAX_RATE_LIMIT_COOLDOWN_MS).
	longestRetryAfter = 30 * time.Minute

	creditRestFirst = 5 * time.Minute
	creditRestMax   = time.Hour
	refusedRest     = 2 * time.Minute
)

// Why a key rests, in web search's words.
const (
	RestRateLimited = "rate_limit"
	RestNoCredits   = "no_credits"
	RestRefused     = "refused"
)

type rest struct {
	until time.Time
	why   string
	// level counts the 429s in a row; creditLevel the answers out of credits in a row.
	level       int
	creditLevel int
}

// providerError is a provider's refusal: its status, and the wait it asked for.
type providerError struct {
	status     int
	retryAfter time.Duration
	message    string
	noCredits  bool
}

func (e *providerError) Error() string {
	switch {
	case e.noCredits:
		return fmt.Sprintf("out of credits (%d)", e.status)
	case e.status == http.StatusTooManyRequests:
		return "rate limited (429)"
	case e.message == "":
		return fmt.Sprintf("answered %d", e.status)
	default:
		return fmt.Sprintf("answered %d: %s", e.status, e.message)
	}
}

// outOfCredit is web-search-plus's wording for an account that is empty, as
// web search reads it (websearch/rest.go).
var outOfCredit = regexp.MustCompile(`(?:^|[^a-z0-9])(?:insufficient[ _](?:credits|funds)|(?:out of|not enough|no) ` +
	`credits|credit balance|not have enough funds)(?:[^a-z0-9]|$)`)

// refusal reads a provider's answer that is not a success.
func refusal(resp *http.Response, body []byte, now time.Time) *providerError {
	return &providerError{
		status: resp.StatusCode, retryAfter: retryAfter(resp.Header.Get("Retry-After"), now),
		message: clip(strings.TrimSpace(string(body)), 200), noCredits: saysNoCredits(resp, body),
	}
}

// saysNoCredits is web search's reading (websearch/rest.go, after
// web-search-plus's _out_of_credit): a 402 always; a 400, 403 or 429 when its
// words say so, but a 429 that names when to come back is a rate limit.
func saysNoCredits(resp *http.Response, body []byte) bool {
	switch resp.StatusCode {
	case http.StatusPaymentRequired:
		return true
	case http.StatusTooManyRequests:
		if strings.TrimSpace(resp.Header.Get("Retry-After")) != "" {
			return false
		}
	case http.StatusBadRequest, http.StatusForbidden:
	default:
		return false
	}

	return outOfCredit.MatchString(strings.ToLower(string(body)))
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

// restingUntil says whether a key rests, until when, and why in words.
func (s *Service) restingUntil(id string) (time.Time, string, bool) {
	r, ok := s.restOf(id)
	if !ok {
		return time.Time{}, "", false
	}

	return r.until, restWords(r.why), true
}

func (s *Service) restOf(id string) (rest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.resting[id]
	if !ok || !r.until.After(s.now()) {
		return rest{}, false
	}

	return r, true
}

func restWords(why string) string {
	switch why {
	case RestNoCredits:
		return "out of credits"
	case RestRefused:
		return "refused"
	default:
		return "rate limited"
	}
}

// failed makes a key rest after a refusal that calls for one, and says
// whether the failure was the key's, so that the provider's next key is asked.
func (s *Service) failed(r route, err error) bool {
	var refused *providerError
	if !errors.As(err, &refused) {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	rs := s.resting[r.id]

	switch {
	case refused.noCredits:
		rs.creditLevel = min(rs.creditLevel+1, restLevels)
		rs.why, rs.until = RestNoCredits, now.Add(min(creditRestFirst<<(rs.creditLevel-1), creditRestMax))
	case refused.status == http.StatusUnauthorized || refused.status == http.StatusForbidden:
		rs.why, rs.until = RestRefused, now.Add(refusedRest)
	case refused.status == http.StatusTooManyRequests:
		rs.level = min(rs.level+1, restLevels)

		wait := refused.retryAfter
		if wait <= 0 {
			wait = min(restFirst<<(rs.level-1), restMax)
		}

		rs.why, rs.until = RestRateLimited, now.Add(wait)
	default:
		return false
	}

	s.resting[r.id] = rs

	return true
}

// served forgets a key's rests in a row.
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
