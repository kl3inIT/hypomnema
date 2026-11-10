package subscription

import "time"

// PollWithoutWaiting makes a device login poll again at once, so a test does
// not wait the seconds the authorization server asks for.
func (c *Codex) PollWithoutWaiting() {
	c.after = func(time.Duration) <-chan time.Time {
		now := make(chan time.Time)
		close(now)

		return now
	}
}

// RefreshWithoutWaiting makes the pause between two accounts' refreshes end
// at once and records how long each pause was meant to be.
func (s *Service) RefreshWithoutWaiting(pauses *[]time.Duration) {
	s.after = func(d time.Duration) <-chan time.Time {
		*pauses = append(*pauses, d)

		now := make(chan time.Time)
		close(now)

		return now
	}
}

// PollWithoutWaiting makes the wait for a new project's operation end at once.
func (a *Antigravity) PollWithoutWaiting() {
	a.after = func(time.Duration) <-chan time.Time {
		now := make(chan time.Time)
		close(now)

		return now
	}
}
