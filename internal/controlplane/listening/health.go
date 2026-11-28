package listening

import "time"

// A source's health, as the Sources screen and a daily live test read it (issue #1289's rule "it shows as working
// only after a real run succeeded"): last30days doctor.audit_state (lib/doctor.py:134-176), run evidence first, a
// source read in full or read empty working, one read in part unverified, one not read not working, and one never run
// unverified, with no source counted working on faith (last30days KEYLESS_ALWAYS_ON is left out: the rule asks for a
// real run); Agent-Reach records a check's result the same way (`check`, `regressed()`). It is kept in memory, as the
// rests are: after a restart every source is unverified until it is read again.

// The health states.
const (
	HealthWorking    = "working"
	HealthUnverified = "unverified"
	HealthNotWorking = "not_working"
)

// Health is one source's state, from the last search that read it.
type Health struct {
	Source string `json:"source"`
	State  string `json:"state"`
	// LastSuccess is when a search last read it, in full, empty or in part.
	LastSuccess *time.Time `json:"last_success,omitempty"`
	// LastFailure is when a search last failed to read one of its streams, with the code and what happened.
	LastFailure *time.Time `json:"last_failure,omitempty"`
	LastCode    Code       `json:"last_code,omitempty"`
	LastDetail  string     `json:"last_detail,omitempty"`
}

// Health is every source's state, in the sources' order.
func (s *Service) Health() []Health {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Health, 0, len(s.platforms))

	for _, p := range s.platforms {
		h, ok := s.health[p.Name]
		if !ok {
			out = append(out, Health{Source: p.Name, State: HealthUnverified})

			continue
		}

		out = append(out, *h)
	}

	return out
}

// record keeps what a search's coverage says of each source it read.
func (s *Service) record(coverage []Coverage, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range coverage {
		if !readAtAll(c) {
			// No call was made (a source off by default, or every backend resting or refused by its probe): no run
			// evidence, so no verdict (last30days doctor.audit_state, lib/doctor.py:143-161).
			continue
		}

		h, ok := s.health[c.Source]
		if !ok {
			h = &Health{Source: c.Source}
			s.health[c.Source] = h
		}

		when := at

		switch c.State {
		case StateOK, StateNoResults:
			h.State = HealthWorking
			h.LastSuccess = &when
		case StatePartial:
			h.State = HealthUnverified
			h.LastSuccess = &when
		default:
			h.State = HealthNotWorking
		}

		if c.Code != "" {
			h.LastFailure, h.LastCode, h.LastDetail = &when, c.Code, failureDetail(c)
		}
	}
}

// failureDetail is what the first backend that failed said, for the source's most actionable code; for a stream
// read in part, what its serving backend said.
func failureDetail(c Coverage) string {
	for _, st := range c.Streams {
		for _, a := range st.Tried {
			if a.Result == string(c.Code) && a.Detail != "" {
				return a.Detail
			}
		}
	}

	for _, st := range c.Streams {
		if st.Code == c.Code && st.ServedBy != "" && len(st.Tried) > 0 {
			return st.Tried[len(st.Tried)-1].Detail
		}
	}

	return ""
}

// readAtAll says some backend of the source was asked in this search: an attempt that was not skipped.
func readAtAll(c Coverage) bool {
	for _, st := range c.Streams {
		for _, a := range st.Tried {
			if !a.Skipped {
				return true
			}
		}
	}

	return false
}
