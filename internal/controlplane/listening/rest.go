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

// rest records a backend's failure and gives until when it rests; zero when it does not. A rest in force after a rate
// limit or a spent quota, the site's own wait, is never shortened nor replaced by another call's failure (a timeout
// of a search canceled while it waited, say): only a later wait of the same kind takes its place, as a site's stop is
// never shortened (StopListeningSource, accounts.go stop; review round 2 of PR #1363; radar
// collection-pacing-contract.md:12, "waiting longer is not permission to retry").
func (s *Service) rest(id string, f *SourceError) time.Time {
	until := restFor(f, s.now())
	if until.IsZero() {
		return until
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.resting[id]; ok && s.now().Before(r.Until) && waits(r.Code) && (!waits(f.Code) ||
		!until.After(r.Until)) {
		return r.Until
	}

	// The rest keeps whether the failure may come back by itself, so a search skipped on it is no more told to the
	// owner than the failure it rests for (review round 3 of PR #1322; Agent-Reach keeps the code a cooldown was made
	// from with it, runtime/manage.py:236-238, runtime/dispatch.py:92-114).
	s.resting[id] = &SourceError{
		Code: f.Code, Detail: "resting after: " + f.Detail, Until: until, transient: f.transient,
	}

	return until
}

// own notes the source whose search rested a path, whose test clears it.
func (s *Service) own(id, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pathOwner[id] = source
}

// clearRests ends a source's rests, its backends' and the paths its searches rested: the owner asks to try it now.
func (s *Service) clearRests(p Platform) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, b := range p.Backends {
		delete(s.resting, p.Name+"/"+b.Name)
	}

	for id, source := range s.pathOwner {
		if source == p.Name {
			delete(s.resting, id)
			delete(s.pathOwner, id)
		}
	}
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

// waits says a failure is the site's own wait: a rate limit or a spent quota.
func waits(c Code) bool { return c == CodeRateLimited || c == CodeLimitReached }
