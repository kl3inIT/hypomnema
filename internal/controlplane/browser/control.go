package browser

import (
	"context"
	"sync"
	"time"
)

// Who drives the browser (issue #219): the agents, or the owner who took
// control. The profile is shared, so the owner holding any tab holds the
// whole browser, and every agent's call is refused meanwhile, reads
// included: the owner may be typing a password. As OpenMausBot's
// ProfileControl (server/browser/control.ts), and not T3 Code's control of
// one tab (apps/server/src/preview/SessionControl.ts).

// The limits of the owner's hold, OpenMausBot's (server/browser/control.ts,
// takeoverMs; server/browser/hold.ts, HOLD_LIMITS).
const (
	// TakeoverWait is how long taking control lets an agent's running call end.
	TakeoverWait = 15 * time.Second
	// HoldGrace is how long the hold outlives a view that dropped.
	HoldGrace = 2 * time.Minute
	// HoldIdle is how long the hold outlives the owner's last input.
	HoldIdle = 5 * time.Minute
)

// Refusal is what an agent's call is answered while the owner holds the
// browser.
const Refusal = "Browser tools are paused while the owner controls this browser. Wait for them to hand control " +
	"back; do not try another browser or another way to reach the page."

// Why the owner's hold ended.
const (
	releasedAsked   = "the owner released it"
	releasedClosed  = "the owner's view was closed"
	releasedDropped = "the owner's view dropped and did not come back"
	releasedIdle    = "the owner sent nothing for a while"
)

type control struct {
	mu sync.Mutex
	// held says the owner drives the browser.
	held bool
	// running counts the agents' calls in flight.
	running int
	// drained is closed when running falls to zero, while a take waits for it.
	drained chan struct{}
	// changed is closed, and made again, each time held changes.
	changed chan struct{}
	// views counts the owner's open views.
	views int
	// dropped is when the last view dropped while the owner held; zero while a view is open.
	dropped time.Time
	// active is when the owner took control or last sent input.
	active time.Time
}

func newControl() *control { return &control{changed: make(chan struct{})} }

// enter starts an agent's call; false when the owner holds the browser.
func (c *control) enter() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.held {
		return false
	}

	c.running++

	return true
}

// leave ends an agent's call and reports whether the owner took the browser
// meanwhile: the call's answer is then the refusal, not what it read.
func (c *control) leave() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running--
	if c.running == 0 && c.drained != nil {
		close(c.drained)
		c.drained = nil
	}

	return c.held
}

// take gives the owner the browser at once, then lets the agents' running
// calls end for up to TakeoverWait.
func (c *control) take(ctx context.Context, now time.Time) {
	c.mu.Lock()

	if !c.held {
		c.held = true
		c.signal()
	}

	c.active, c.dropped = now, time.Time{}

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

// release gives the browser back to the agents; false when the owner did not
// hold it.
func (c *control) release() bool {
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
func (c *control) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// state is whether the owner holds the browser, and a channel closed when
// that changes.
func (c *control) state() (bool, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.held, c.changed
}

// input notes the owner's input; false when the owner does not hold the
// browser, and the input is to be dropped.
func (c *control) input(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.held {
		c.active = now
	}

	return c.held
}

// opened counts a view in; false when there are as many as may be.
func (c *control) opened(limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.views >= limit {
		return false
	}

	c.views++
	c.dropped = time.Time{}

	return true
}

// unopened takes back a view that did not open.
func (c *control) unopened() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.views--
}

// closed counts a view out. When it was the last one, a view the owner
// closed ends the hold at once, and one that dropped starts the grace. It
// returns why the hold ended, or "".
func (c *control) closed(clean bool, now time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.views--
	if c.views > 0 || !c.held {
		return ""
	}

	if !clean {
		c.dropped = now

		return ""
	}

	c.held = false
	c.signal()

	return releasedClosed
}

// expire ends a hold that outlived its view or the owner's last input, and
// returns why, or "".
func (c *control) expire(now time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.held {
		return ""
	}

	var reason string

	switch {
	case !c.dropped.IsZero() && now.Sub(c.dropped) >= HoldGrace:
		reason = releasedDropped
	case now.Sub(c.active) >= HoldIdle:
		reason = releasedIdle
	default:
		return ""
	}

	c.held = false
	c.signal()

	return reason
}
