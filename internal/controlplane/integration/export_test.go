package integration

import "time"

// WithoutPollPauses makes a device sign-in poll at once instead of waiting between two polls, and records each
// pause it would have taken.
func (s *Service) WithoutPollPauses(paused *[]time.Duration) *Service {
	s.after = func(d time.Duration) <-chan time.Time {
		*paused = append(*paused, d)

		c := make(chan time.Time, 1)
		c <- time.Time{}

		return c
	}

	return s
}
