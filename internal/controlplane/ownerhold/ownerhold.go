// Package ownerhold is who drives a thing the agents use and the owner
// watches: the agents, or the owner who took control (issue #219 for the
// browser, issue #417 for the phone). While the owner holds it every agent's
// call on it is refused, reads included: the owner may be typing a password.
// The phone has one Hold, as OpenMausBot's ProfileControl holds a whole
// browser (server/browser/control.ts); the browser has one a tab, as T3
// Code's SessionControl is one tab's (apps/server/src/preview/SessionControl.ts;
// issue #1113). The browser kept it as its own until the phone took the same
// rules (issue #417: "same release rules as the browser").
package ownerhold

import (
	"context"
	"sync"
	"time"
)

// The limits of the owner's hold, OpenMausBot's (server/browser/control.ts,
// takeoverMs; server/browser/hold.ts, HOLD_LIMITS).
const (
	// TakeoverWait is how long taking control lets an agent's running call end.
	TakeoverWait = 15 * time.Second
	// Grace is how long the hold outlives the owner's last view, however it
	// went: a socket that dropped, or a page that closed it, as switching to
	// another tab in a pane does (issue #603).
	Grace = 2 * time.Minute
	// Idle is how long the hold outlives the owner's last input.
	Idle = 5 * time.Minute
	// CheckEvery is how often a holder looks for the hold's end.
	CheckEvery = 5 * time.Second
)

// Why the owner's hold ended.
const (
	ReleasedAsked = "the owner released it"
	ReleasedGone  = "the owner's last view went and none came back"
	ReleasedIdle  = "the owner sent nothing for a while"
)

// Hold is who drives one shared tool. The zero value is not ready: use New.
type Hold struct {
	mu sync.Mutex
	// held says the owner drives the tool.
	held bool
	// running counts the agents' calls in flight.
	running int
	// drained is closed when running falls to zero, while a take waits for it.
	drained chan struct{}
	// changed is closed, and made again, each time held changes.
	changed chan struct{}
	// views counts the owner's open views.
	views int
	// gone is when the last view went while the owner held; zero while a view is open.
	gone time.Time
	// active is when the owner took control or last sent input.
	active time.Time
}

// New returns a Hold the agents drive.
func New() *Hold { return &Hold{changed: make(chan struct{})} }

// Enter starts an agent's call; false when the owner holds the tool.
func (c *Hold) Enter() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.held {
		return false
	}

	c.running++

	return true
}

// Leave ends an agent's call and reports whether the owner took the tool
// meanwhile: the call's answer is then the refusal, not what it read.
func (c *Hold) Leave() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running--
	if c.running == 0 && c.drained != nil {
		close(c.drained)
		c.drained = nil
	}

	return c.held
}

// Take gives the owner the tool at once, then lets the agents' running calls
// end for up to TakeoverWait.
func (c *Hold) Take(ctx context.Context, now time.Time) {
	c.mu.Lock()

	if !c.held {
		c.held = true
		c.signal()
	}

	c.active, c.gone = now, time.Time{}

	var drained chan struct{}

	if c.running > 0 {
		if c.drained == nil {
			c.drained = make(chan struct{})
		}

		drained = c.drained
	}

	c.mu.Unlock()

	if drained == nil {
		return
	}

	wait := time.NewTimer(TakeoverWait)
	defer wait.Stop()

	select {
	case <-drained:
	case <-wait.C:
	case <-ctx.Done():
	}
}

// Release gives the tool back to the agents; false when the owner did not
// hold it.
func (c *Hold) Release() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.held {
		return false
	}

	c.held = false
	c.signal()

	return true
}

// signal tells the views that held changed. The caller holds mu.
func (c *Hold) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// State is whether the owner holds the tool, and a channel closed when that
// changes.
func (c *Hold) State() (bool, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.held, c.changed
}

// Input notes the owner's input; false when the owner does not hold the
// tool, and the input is to be dropped.
func (c *Hold) Input(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.held {
		c.active = now
	}

	return c.held
}

// Opened counts a view in; false when there are as many as may be.
func (c *Hold) Opened(limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.views >= limit {
		return false
	}

	c.views++
	c.gone = time.Time{}

	return true
}

// Unopened takes back a view that did not open.
func (c *Hold) Unopened() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.views--
}

// Unused says that nothing uses the Hold: the owner does not hold it, no
// agent's call is in it and no view is open.
func (c *Hold) Unused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return !c.held && c.running == 0 && c.views == 0
}

// Viewing says whether a view is open.
func (c *Hold) Viewing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.views > 0
}

// Closed counts a view out. When it was the last one while the owner held,
// the grace starts, however the view went: only Release, the owner's hand
// back, ends the hold at once. As OpenMausBot's phone
// (server/routes/phone-screen.ts, the last stream's close calls
// hold.watched(false); server/browser/hold.ts, watched). Its browser ends a
// hold at once on a view closed on purpose; here the browser keeps it too
// (owner, 2026-11-16, issue #603).
func (c *Hold) Closed(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.views--
	if c.views > 0 || !c.held {
		return
	}

	c.gone = now
}

// Expire ends a hold that outlived its view or the owner's last input, and
// returns why, or "".
func (c *Hold) Expire(now time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.held {
		return ""
	}

	var reason string

	switch {
	case !c.gone.IsZero() && now.Sub(c.gone) >= Grace:
		reason = ReleasedGone
	case now.Sub(c.active) >= Idle:
		reason = ReleasedIdle
	default:
		return ""
	}

	c.held = false
	c.signal()

	return reason
}
