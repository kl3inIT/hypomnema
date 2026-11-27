package speech

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"time"
)

// A refusal that may pass is asked again, as the Fern-made ElevenLabs SDK asks (elevenlabs-python
// src/elevenlabs/core/http_client.py:99-123, max_retries 2 at :379) and the statuses Soniox says to retry
// (docs.soniox.com /api-reference/tts/generate_tts errors; issue #1285, row 4):
//
//   - 408, 429, 500 and 503 are asked again, twice at most;
//   - after the Retry-After the answer named, else after 1 s, doubling, moved by up to ±10 % (the SDK's symmetric
//     jitter of JITTER_FACTOR 0.2 around the backoff, at most a minute);
//   - a Retry-After over 30 s is not waited for: the key rests that long and the provider's next key is asked
//     (rest.go). Hypomnema's own choice, since its keys are a pool where the SDK has one key;
//   - an account out of credits is not asked again, nor a body that cannot be sent again.
//
// A refusal still there after the last is the key's, as before: it rests (rest.go).
const (
	retries        = 2
	retryFirstWait = time.Second
	retryLongest   = time.Minute
	retryJitter    = 0.2
	// retryLongestAfter is the longest Retry-After waited for before asking again.
	retryLongestAfter = 30 * time.Second
)

// retryStatuses are the statuses asked again.
var retryStatuses = []int{
	http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
	http.StatusServiceUnavailable,
}

// errNoReplay: the request's body was a stream and cannot be sent again.
var errNoReplay = errors.New("the request cannot be sent again")

// retrying runs attempt, numbered from 0, until it answers, fails in a way that does not pass, or is asked twice
// again. The last failure is returned.
func (s *Service) retrying(ctx context.Context, attempt func(n int) error) error {
	var last error

	for n := 0; ; n++ {
		err := attempt(n)
		if errors.Is(err, errNoReplay) {
			return last
		}

		wait, ok := s.retryWait(err, n)
		if !ok {
			return err
		}

		last = err

		if s.sleep(ctx, wait) != nil {
			return err
		}
	}
}

// retryWait says whether a failure is asked again after the n-th attempt, and after how long.
func (s *Service) retryWait(err error, n int) (time.Duration, bool) {
	var refused *providerError

	switch {
	case n >= retries, !errors.As(err, &refused), refused.noCredits:
		return 0, false
	case !slices.Contains(retryStatuses, refused.status):
		return 0, false
	case refused.retryAfter > retryLongestAfter:
		return 0, false
	case refused.retryAfter > 0:
		return refused.retryAfter, true
	}

	backoff := min(float64(retryFirstWait)*math.Pow(2, float64(n)), float64(retryLongest))

	return time.Duration(backoff * (1 + s.jitter()*retryJitter)), true
}
