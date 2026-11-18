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
	// wires are the API each model is served on, for a vendor that names one (issue #854).
	wires map[string]string
	// names are the models served, in order (issue #623).
	names []string
	at    time.Time
	tried time.Time
}

// ServedLimits gives the context window and largest output the vendor serves a model with under a runtime's
// accounts (issue #587): the first active account logged in here that answers says, as omp takes maxTokens and
// maxOutputTokens from fetchAvailableModels. ok is false when nothing is known.
func (s *Service) ServedLimits(ctx context.Context, runtime, model string) (window, output int64, ok bool) {
	l, ok := s.reading(ctx, runtime).limits[model]

	return l[0], l[1], ok
}

// ServedWire is the API the vendor serves a model on under a runtime's accounts (issue #854), read as ServedLimits
// reads. ok is false when nothing is known.
func (s *Service) ServedWire(ctx context.Context, runtime, model string) (wire string, ok bool) {
	wire, ok = s.reading(ctx, runtime).wires[model]

	return wire, ok
}

// ServedNames are the models the vendor serves a runtime's accounts, by name: the first active account logged in
// here that answers says (issue #623), read as ServedLimits reads and kept as long. ok is false when nothing is
// known: no account, or none answered.
func (s *Service) ServedNames(ctx context.Context, runtime string) (names []string, ok bool) {
	r := s.reading(ctx, runtime)

	return r.names, !r.at.IsZero()
}

// reading is the last reading of what a runtime's accounts are served, read again when it is older than servedKept
// and the last try is older than servedRetry.
func (s *Service) reading(ctx context.Context, runtime string) servedReading {
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
		if read, ok := s.readServed(ctx, runtime); ok {
			read.at, read.tried = now, now

			s.servedMu.Lock()
			r = read
			s.served[runtime] = r
			s.servedMu.Unlock()
		}
	}

	return r
}

// readServed asks the vendor, under the runtime's accounts in turn, what each model is served with.
func (s *Service) readServed(ctx context.Context, runtime string) (servedReading, bool) {
	accounts, err := s.List(ctx)
	if err != nil {
		return servedReading{}, false
	}

	for _, a := range accounts {
		if a.Runtime != runtime || a.State != StateActive {
			continue
		}

		models, err := s.Models(ctx, a.ID)
		if err != nil {
			continue
		}

		read := servedReading{
			limits: map[string][2]int64{}, wires: map[string]string{}, names: make([]string, 0, len(models)),
		}

		for _, m := range models {
			read.names = append(read.names, m.Name)

			if m.Wire != "" {
				read.wires[m.Name] = m.Wire
			}

			if m.Window > 0 || m.Output > 0 {
				read.limits[m.Name] = [2]int64{m.Window, m.Output}
			}
		}

		return read, true
	}

	return servedReading{}, false
}
