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
