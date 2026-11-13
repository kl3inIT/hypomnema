package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// A tab fills the pane the owner watches it in (issue #193), as T3 Code's and
// OpenMausBot's views do: the page sends the pane's size as it changes, and
// the tab takes the pane's shape. As OpenMausBot's fill
// (server/browser/viewers/viewport.ts, fillPage), the tab is never narrower
// than a desktop, so a site keeps the layout the agents working in it know
// while the owner watches from a phone or a narrow pane; the view scales it
// down to the pane. T3 Code gives the tab the pane's own size, which suits a
// browser only a person uses.

// standardPage is the size agent-browser gives a tab (cli/src/native/actions.rs,
// handle_viewport), which a tab goes back to when nobody watches it.
var standardPage = Size{Width: 1280, Height: 720}

// Bounds of a pane and of the page it gives, OpenMausBot's VIEWPORT_LIMITS
// and T3 Code's resize: a pane outside them (a collapsed one, say) leaves the
// tab as it is.
const (
	minPane = 100
	maxPage = 3840
	// maxFillRatio is how many times as tall as wide a filled page is at
	// most; a taller pane shows bands above and below it (OpenMausBot).
	maxFillRatio = 2
	// viewportTimeout bounds setting a tab's size.
	viewportTimeout = 30 * time.Second
)

// toolViewport is agent-browser's tool that sets a tab's size. It is in its
// `mobile` set, not the `core` one agents are offered: only the owner's view
// sets a size.
const toolViewport = "agent_browser_set_viewport"

// fillPage is the page that fills a pane of the given CSS size: the pane's
// shape at least a desktop wide, at most twice as tall as wide. ok is false
// for a pane out of bounds.
func fillPage(width, height float64) (Size, bool) {
	if math.IsNaN(width) || math.IsNaN(height) || width < minPane || height < minPane ||
		width > maxPage || height > maxPage {
		return Size{}, false
	}

	w := max(int(math.Round(width)), standardPage.Width)
	h := min(int(math.Round(height*float64(w)/width)), maxFillRatio*w, maxPage)

	return Size{Width: w, Height: h}, true
}

// resize gives a session's tab a size, unless it has it already.
func (e *Engine) resize(ctx context.Context, s *session, size Size) error {
	e.mu.Lock()
	cdp := e.cdp
	e.mu.Unlock()

	if cdp == "" {
		return ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.released || s.size == size || (s.size == Size{} && size == standardPage) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, viewportTimeout)
	defer cancel()

	arguments, err := json.Marshal(map[string]int{"width": size.Width, "height": size.Height})
	if err != nil {
		return err
	}

	out, err := run(ctx, command{
		Path: e.binary, Args: []string{"mcp", "call", toolViewport, "-", "--tools", "mobile"},
		Env: e.env(s.name, cdp), Stdin: string(arguments),
	})
	if err != nil {
		return fmt.Errorf("%w: setting the tab's size: %w", ErrUnavailable, err)
	}

	if out.Code != 0 {
		return fmt.Errorf("%w: setting the tab's size ended with code %d: %s", ErrUnavailable, out.Code,
			lastLine(string(out.Stderr)))
	}

	s.size = size

	return nil
}

// unwatched gives a tab nobody watches any more its standard size back, as
// OpenMausBot's fill does with no viewer: an agent's screenshots and clicks
// go back to the page it knows.
func (e *Engine) unwatched(ctx context.Context, s *session) {
	e.mu.Lock()
	watched := s.views > 0
	e.mu.Unlock()

	if watched {
		return
	}

	// The view has ended, maybe with its context: setting the size back outlives it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), viewportTimeout)
	defer cancel()

	if err := e.resize(ctx, s, standardPage); err != nil {
		e.cfg.Logger.WarnContext(ctx, "a tab's size could not be set back",
			slog.String("event", "browser.resize_failed"), slog.String("session", s.name), slog.Any("err", err))
	}
}
