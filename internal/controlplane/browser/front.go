package browser

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Which tab is in front of Chrome's window (issue #1113). Every tab is in the
// one window, and Chrome paints only the tab in front: the stream of a tab
// behind another sends no frame. A view brings its tab to the front as it
// opens, and when the view opened last closes, the one open before it brings
// its own back, so the views left keep streaming.
//
// Bringing a tab to the front takes the focus from the tab that had it, which
// a page may notice mid-way through an agent's call (a wait for text that
// needs the focus failed with it, review of PR #1233). So it is the owner's
// action queued behind the agents' calls running in the browser, as
// OpenMausBot's viewer brings a tab to the front through its control
// (server/browser/viewers/core.ts:845, tabSelect: control.human(... showTab)),
// which T3 Code's SessionControl queues after the running work
// (apps/server/src/preview/SessionControl.ts, action and enqueue). A call
// asked for while a tab is being brought to the front waits for it, and the
// calls running are waited for at most TakeoverWait, as a takeover drains them
// (OpenMausBot's takeoverMs). Moves asked for while one waits replace it and
// share its wait: only the newest view's tab is brought forward, at the end of
// the first one's wait. Making the owner's tab, which comes in front as it is
// made, waits in the same queue (view.go, viewed).

// focus says who may move the focus: the agents' calls running, and the work
// queued in front of them, a move to the front or the making of a tab. The
// zero value is ready.
type focus struct {
	mu sync.Mutex
	// running counts the agents' calls in flight.
	running int
	// idle is closed when running falls to zero while queued work waits for it.
	idle chan struct{}
	// queued is closed when the queued work ends; nil while none is queued. A call asked for meanwhile waits.
	queued chan struct{}
	// moving says a move to the front is queued; want is the newest move asked for, nil once made, and wake
	// tells the move that want changed. Moves asked for while one waits replace it and share its wait.
	moving bool
	want   *wanted
	wake   chan struct{}
	// wait is how long queued work waits for the calls running: TakeoverWait when zero.
	wait time.Duration
	// views are the open views, the one opened last at the end.
	views []*View
}

// wanted is a move to the front asked for: bring makes it, and gone is closed
// once it is no longer wanted (its view closed).
type wanted struct {
	gone  <-chan struct{}
	bring func()
}

func (f *focus) waitFor() time.Duration {
	if f.wait > 0 {
		return f.wait
	}

	return TakeoverWait
}

// enter starts an agent's call once no work is queued in front of it.
func (f *focus) enter(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for f.queued != nil {
		queued := f.queued

		f.mu.Unlock()
		select {
		case <-queued:
		case <-ctx.Done():
			f.mu.Lock()

			return ctx.Err()
		}
		f.mu.Lock()
	}

	f.running++

	return nil
}

// leave ends an agent's call.
func (f *focus) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.running--
	if f.running == 0 && f.idle != nil {
		close(f.idle)
		f.idle = nil
	}
}

// idled is closed when the calls running have ended. The caller holds mu.
func (f *focus) idled() chan struct{} {
	if f.idle == nil {
		f.idle = make(chan struct{})
	}

	return f.idle
}

// queue puts work in front of the agents' calls once the work queued before it ended; false when ctx ended
// first. The caller holds mu, which queue may let go of while it waits.
func (f *focus) queue(ctx context.Context) bool {
	for f.queued != nil {
		queued := f.queued

		f.mu.Unlock()
		select {
		case <-queued:
		case <-ctx.Done():
			f.mu.Lock()

			return false
		}
		f.mu.Lock()
	}

	f.queued = make(chan struct{})

	return true
}

// unqueue ends the queued work: the calls waiting go on. The caller holds mu.
func (f *focus) unqueue() {
	close(f.queued)
	f.queued = nil
}

// alone runs do in front of the agents' calls: once those running have ended,
// or after the wait, with the calls asked for meanwhile waiting for it.
func (f *focus) alone(ctx context.Context, do func() error) error {
	f.mu.Lock()

	if !f.queue(ctx) {
		f.mu.Unlock()

		return ctx.Err()
	}

	if f.running > 0 {
		idle := f.idled()
		f.mu.Unlock()

		wait := time.NewTimer(f.waitFor())

		select {
		case <-idle:
		case <-wait.C:
		case <-ctx.Done():
		}

		wait.Stop()
		f.mu.Lock()
	}

	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.unqueue()
		f.mu.Unlock()
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	return do()
}

// move brings a tab to the front with bring once the agents' calls running
// have ended, or after the wait, for as long as ctx lasts. A move asked for
// while another waits replaces it: the newest is made, at the end of the
// first one's wait (review of PR #1233).
func (f *focus) move(ctx context.Context, bring func(context.Context)) {
	f.mu.Lock()
	f.want = &wanted{gone: ctx.Done(), bring: func() { bring(ctx) }}

	if f.moving {
		select {
		case f.wake <- struct{}{}:
		default:
		}

		f.mu.Unlock()

		return
	}

	f.moving = true
	f.wake = make(chan struct{}, 1)
	defer func() {
		f.moving = false
		f.mu.Unlock()
	}()

	// The move outlives the view that asked for it: a newer one may replace it.
	if !f.queue(context.WithoutCancel(ctx)) {
		return
	}
	defer f.unqueue()

	deadline := time.NewTimer(f.waitFor())
	defer deadline.Stop()

	late := false

	for {
		w := f.want

		select {
		case <-w.gone:
			// Its view closed: nothing newer is wanted.
			f.want = nil

			return
		default:
		}

		if f.running > 0 && !late {
			idle, wake := f.idled(), f.wake

			f.mu.Unlock()
			select {
			case <-idle:
			case <-wake:
			case <-w.gone:
			case <-deadline.C:
				late = true
			}
			f.mu.Lock()

			continue
		}

		f.want = nil
		f.mu.Unlock()
		w.bring()
		f.mu.Lock()

		// A move asked for while this one was made has a wait of its own.
		if f.want == nil {
			return
		}

		deadline.Reset(f.waitFor())
		late = false
	}
}

// opened counts a view in as the one opened last.
func (f *focus) opened(v *View) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.views = append(f.views, v)
}

// closed counts a view out, and returns the view to bring to the front
// instead: the one opened before it, when it was the one opened last.
func (f *focus) closed(v *View) *View {
	f.mu.Lock()
	defer f.mu.Unlock()

	i := slices.Index(f.views, v)
	if i < 0 {
		return nil
	}

	last := i == len(f.views)-1
	f.views = slices.Delete(f.views, i, i+1)

	if !last || len(f.views) == 0 {
		return nil
	}

	return f.views[len(f.views)-1]
}

// front brings a view's tab to the front, once the agents' calls running have
// ended, for as long as the view is open. A tab not brought there leaves the
// view as it was: it goes on.
func (e *Engine) front(v *View) {
	e.focus.move(v.life, func(ctx context.Context) {
		if err := e.activate(ctx, v.s); err != nil {
			e.cfg.Logger.WarnContext(ctx, "a tab could not be brought to the front",
				slog.String("event", "browser.front_failed"), slog.String("tab", v.tab), slog.Any("err", err))
		}
	})
}

// activate brings a session's tab to the front with the browser's own
// Target.activateTarget, as agent-browser revives a tab with it
// (cli/src/native/browser.rs:1233) and its tab switch brings the tab to the
// front (Page.bringToFront).
func (e *Engine) activate(ctx context.Context, s *session) error {
	e.mu.Lock()
	cdp := e.cdp
	e.mu.Unlock()

	target, err := e.boundTarget(s)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, frontTimeout)
	defer cancel()

	conn, err := dialCDP(ctx, cdp, nil)
	if err != nil {
		return err
	}
	defer conn.close()

	return conn.send(ctx, "Target.activateTarget", map[string]any{"targetId": target}, "", nil)
}
