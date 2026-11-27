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
// (OpenMausBot's takeoverMs).

// focus says who may move the focus: the agents' calls running, and a move
// waiting for them. The zero value is ready.
type focus struct {
	mu sync.Mutex
	// running counts the agents' calls in flight.
	running int
	// idle is closed when running falls to zero while a move waits for it.
	idle chan struct{}
	// moving is closed when the move under way ends; nil while none is.
	moving chan struct{}
	// asked counts the moves asked for: one that a newer move follows is not made.
	asked uint64
	// views are the open views, the one opened last at the end.
	views []*View
}

// enter starts an agent's call once no move is under way.
func (f *focus) enter(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for f.moving != nil {
		moving := f.moving

		f.mu.Unlock()
		select {
		case <-moving:
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

// move runs bring once the agents' calls running have ended, or after
// TakeoverWait, unless a newer move was asked for meanwhile or ctx ended.
func (f *focus) move(ctx context.Context, bring func(context.Context)) {
	f.mu.Lock()
	f.asked++
	asked := f.asked

	for f.moving != nil {
		moving := f.moving

		f.mu.Unlock()
		select {
		case <-moving:
		case <-ctx.Done():
			return
		}
		f.mu.Lock()
	}

	if asked != f.asked {
		f.mu.Unlock()

		return
	}

	moving := make(chan struct{})
	f.moving = moving

	var idle chan struct{}

	if f.running > 0 {
		if f.idle == nil {
			f.idle = make(chan struct{})
		}

		idle = f.idle
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.moving = nil
		f.mu.Unlock()
		close(moving)
	}()

	if idle != nil {
		wait := time.NewTimer(TakeoverWait)
		defer wait.Stop()

		select {
		case <-idle:
		case <-wait.C:
		case <-ctx.Done():
			return
		}
	}

	bring(ctx)
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
