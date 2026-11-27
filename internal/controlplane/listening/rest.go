package listening

import "time"

// How long a backend rests after a failure, skipped by every search until then (rest.go). A refusal of the network
// is not retried for 6 hours (last30days reddit_health.py SKIP_TTL_SECONDS); a rate limit waits what the source
// said, else an hour (Agent-Reach RATE_LIMIT_SECONDS, runtime/dispatch.py:30); a spent daily quota until 00:00 UTC
// (the web search's daily rest, websearch/rest.go); a timeout a minute and a broken answer two (Agent-Reach
// BACKEND_PAUSE_SECONDS, dispatch.py:29). A sign-in wall, a missing page and a clean zero do not rest: Agent-Reach
// does not pause them either.
const (
	RefusedRest     = 6 * time.Hour
	RateLimitedRest = time.Hour
	TimeoutRest     = time.Minute
	BrokenRest      = 2 * time.Minute
)

// restFor is until when a failure rests its backend; zero: it does not.
func restFor(f *SourceError, now time.Time) time.Time {
	// A wait the source or the web search chain named is kept, whatever the code (Agent-Reach keeps the platform's
	// retry_after_seconds, runtime/dispatch.py:89-99).
	if f.Until.After(now) && f.Code != CodeNeedLogin && f.Code != CodeNotFound && f.Code != CodeNoResults {
		return f.Until
	}

	switch f.Code {
	case CodeRefused:
		return now.Add(RefusedRest)
	case CodeRateLimited:
		if f.Until.After(now) {
			return f.Until
		}

		return now.Add(RateLimitedRest)
	case CodeLimitReached:
		if f.Until.After(now) {
			return f.Until
		}

		return now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	case CodeTimeout:
		return now.Add(TimeoutRest)
	case CodeBroken:
		return now.Add(BrokenRest)
	default:
		return time.Time{}
	}
}

// rest records a backend's failure and gives until when it rests; zero when it does not.
func (s *Service) rest(id string, f *SourceError) time.Time {
	until := restFor(f, s.now())
	if until.IsZero() {
		return until
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.resting[id] = &SourceError{Code: f.Code, Detail: "resting after: " + f.Detail, Until: until}

	return until
}

// restOf is the rest a backend is in, nil when it may be asked.
func (s *Service) restOf(id string) *SourceError {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.resting[id]
	if !ok {
		return nil
	}

	if !s.now().Before(r.Until) {
		delete(s.resting, id)

		return nil
	}

	return r
}
