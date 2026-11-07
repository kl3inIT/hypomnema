package platform

import "time"

// Clock tells the time. Code that decides on time (leases, timeouts, the
// orchestration core) takes one, so tests can set the time exactly.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }
