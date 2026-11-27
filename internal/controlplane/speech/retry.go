package speech

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// An ElevenLabs request that is rate limited (429) or fails on its side (5xx) is asked again twice before its
// failure counts (issue #1283, row 6), as ElevenLabs's own SDK does (elevenlabs-python core/http_client.py:99-123,
// 379-382): it waits the Retry-After the answer named, at most 60 seconds, else 1 second doubling with each retry.
// Only then does the key rest and the next key or provider get asked, as before (rest.go, each). The SDK's jitter
// is left out: one control plane asks, so there is no herd to spread.
const (
	retries        = 2
	retryFirst     = time.Second
	retryLongest   = 60 * time.Second
	retryNotBefore = time.Millisecond
)

// retrying asks once, then again after a refusal that may pass, at most retries times.
func (s *Service) retrying(ctx context.Context, ask func() error) error {
	for n := 0; ; n++ {
		err := ask()

		wait, again := retryWait(err, n)
		if !again || n == retries {
			return err
		}

		if err := s.cfg.Wait(ctx, wait); err != nil {
			return err
		}
	}
}

// retryWait is how long to wait before asking again after the nth retry's error, and whether to.
func retryWait(err error, n int) (time.Duration, bool) {
	var refused *providerError
	if !errors.As(err, &refused) || refused.noCredits ||
		(refused.status != http.StatusTooManyRequests && refused.status < http.StatusInternalServerError) {
		return 0, false
	}

	if refused.retryAfter >= retryNotBefore {
		return min(refused.retryAfter, retryLongest), true
	}

	return min(retryFirst<<n, retryLongest), true
}

// wait sleeps for d, or until ctx ends.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
