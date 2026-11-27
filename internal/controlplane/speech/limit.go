package speech

import (
	"context"
	"sync"
	"time"
)

// Soniox's text-to-speech takes at most 3 requests at once and 100 a minute (docs.soniox.com
// /tts/rest-api/limits-and-quotas; issue #1285, row 6). Each key keeps to them on its own, since a key is one
// project's: a request waits for a place rather than be refused with a 429. The limits are in memory.
const (
	sonioxTTSConcurrent = 3
	sonioxTTSPerMinute  = 100
	limitWindow         = time.Minute
)

// limiter lets at most concurrent requests run at once, and at most perWindow start in any window.
type limiter struct {
	concurrent int
	perWindow  int
	window     time.Duration
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	running int
	// started are the starts within the last window, the oldest first.
	started []time.Time
	// freed is closed and replaced when a request ends, to wake those waiting for a place.
	freed chan struct{}
}

func newLimiter(concurrent, perWindow int, window time.Duration, now func() time.Time,
	sleep func(ctx context.Context, d time.Duration) error,
) *limiter {
	return &limiter{
		concurrent: concurrent, perWindow: perWindow, window: window, now: now, sleep: sleep,
		freed: make(chan struct{}),
	}
}

// acquire waits for a place and returns its release, or ctx's error.
func (l *limiter) acquire(ctx context.Context) (func(), error) {
	for {
		l.mu.Lock()

		now := l.now()
		for len(l.started) > 0 && !l.started[0].Add(l.window).After(now) {
			l.started = l.started[1:]
		}

		switch {
		case l.running >= l.concurrent:
			freed := l.freed
			l.mu.Unlock()

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-freed:
			}
		case len(l.started) >= l.perWindow:
			wait := l.started[0].Add(l.window).Sub(now)
			l.mu.Unlock()

			if err := l.sleep(ctx, wait); err != nil {
				return nil, err
			}
		default:
			l.running++
			l.started = append(l.started, now)
			l.mu.Unlock()

			var once sync.Once

			return func() { once.Do(l.release) }, nil
		}
	}
}

func (l *limiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.running--
	close(l.freed)
	l.freed = make(chan struct{})
}

// sonioxTTSLimit is a key's Soniox text-to-speech limiter.
func (s *Service) sonioxTTSLimit(k Key) *limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.limits[k.Name]
	if !ok {
		l = newLimiter(sonioxTTSConcurrent, sonioxTTSPerMinute, limitWindow, s.now, s.sleep)
		s.limits[k.Name] = l
	}

	return l
}
