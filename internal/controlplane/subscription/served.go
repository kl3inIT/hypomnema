package subscription

import (
	"context"
	"time"
)

// servedKept is how long a reading of what a runtime's accounts are served is given again, and servedRetry how
// long after a failed reading the vendor is asked again. omp reads an account's roster once per discovery and keeps
// it (packages/catalog/src/discovery/antigravity.ts); a model's window changes rarely.
const (
	servedKept  = time.Hour
	servedRetry = 5 * time.Minute
)

// servedReading is the last reading of each model's window and largest output that a runtime's accounts are
// served with.
type servedReading struct {
	limits map[string][2]int64
	at     time.Time
	tried  time.Time
}

// ServedLimits gives the context window and largest output the vendor serves a model with under a runtime's
// accounts (issue #587): the first active account logged in here that answers says, as omp takes maxTokens and
// maxOutputTokens from fetchAvailableModels. ok is false when nothing is known.
func (s *Service) ServedLimits(ctx context.Context, runtime, model string) (window, output int64, ok bool) {
	now := s.clock.Now()

	s.servedMu.Lock()
	r := s.served[runtime]
	stale := now.Sub(r.at) >= servedKept && now.Sub(r.tried) >= servedRetry

	if stale {
		r.tried = now
		s.served[runtime] = r
	}
	s.servedMu.Unlock()

	if stale {
		if limits, read := s.readServed(ctx, runtime); read {
			s.servedMu.Lock()
			r = servedReading{limits: limits, at: now, tried: now}
			s.served[runtime] = r
			s.servedMu.Unlock()
		}
	}

	l, ok := r.limits[model]

	return l[0], l[1], ok
}

// readServed asks the vendor, under the runtime's accounts in turn, what each model is served with.
func (s *Service) readServed(ctx context.Context, runtime string) (map[string][2]int64, bool) {
	accounts, err := s.List(ctx)
	if err != nil {
		return nil, false
	}

	for _, a := range accounts {
		if a.Runtime != runtime || a.State != StateActive {
			continue
		}

		models, err := s.Models(ctx, a.ID)
		if err != nil {
			continue
		}

		limits := map[string][2]int64{}

		for _, m := range models {
			if m.Window > 0 || m.Output > 0 {
				limits[m.Name] = [2]int64{m.Window, m.Output}
			}
		}

		return limits, true
	}

	return nil, false
}
