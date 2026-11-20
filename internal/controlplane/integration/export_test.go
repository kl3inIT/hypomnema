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

// BetweenReads runs do between the reads of a BigQuery Connection's settings and of its key.
func (s *Service) BetweenReads(do func()) *Service {
	s.betweenReads = do

	return s
}

// WithWriteTimeout bounds a BigQuery Connection's writes by d in place of 30 seconds.
func (s *Service) WithWriteTimeout(d time.Duration) *Service {
	s.saWriteTimeout = d

	return s
}
