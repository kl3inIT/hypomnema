package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Filming an agent's tab for a take (issue #1189, rows 1, 4 to 7). The frames
// are Chrome's own screencast, read on a DevTools connection of the engine's
// own (cdp.go) attached to the tab agent-browser pinned the agent's session to,
// as autodemo films its page (src/perform.js:213-224, Page.startScreencast in
// JPEG at quality 90, as large as the viewport, every frame, each acknowledged
// as it comes). The tab is given the take's viewport by agent-browser itself,
// as the owner's view gives it a size (fill.go), so the session's own idea of
// its viewport, and its clicks, stay right; while it films, the owner's view
// does not resize it, and its size before is given back when it stops. No
// mobile emulation: a phone's layout is a phone-sized viewport, as in every
// reference (row 6). The take never leaves the browser's context: the tab is
// the agent's own, with the shared profile's cookies and the gate's rules.
//
// The frames are as large as the viewport in CSS pixels, as agent-demo-video's
// (src/capture.ts:285, its screencast's size the viewport), not at autodemo's
// device scale factor of 2 (src/browser.js:7), which the design asked for:
// Chrome 158's screencast draws no frame larger than the view in CSS pixels,
// whatever the page's devicePixelRatio, on the shared Chrome and on the headless
// shell alike, and the one override that made it draw at twice the size
// (Emulation.setDeviceMetricsOverride's scale with dontSetVisibleSize on a
// window twice as large) made agent-browser's clicks miss (tried 2026-11-26,
// agent-browser 0.42.0).

const (
	// filmQuality is the frames' JPEG quality (autodemo's and agent-demo-video's 90).
	filmQuality = 90
	// filmStep bounds one DevTools command of a film.
	filmStep = 30 * time.Second
	// toolBox is agent-browser's tool that reads an element's box. It is in no set but `all`.
	toolBox = "agent_browser_get_box"
)

// Errors of a film.
var (
	// ErrNoPage reports an agent with no tab of its own yet: a take films a page the agent opened.
	ErrNoPage = errors.New("open a page with agent_browser_open first: a take films your own tab")
	// ErrFilmingTab reports a tab that is filmed already.
	ErrFilmingTab = errors.New("your tab is being filmed already")
)

// Frame is one frame of a film: its JPEG and when Chrome drew it.
type Frame struct {
	JPEG []byte
	At   time.Time
}

// Box is an element's box in the page's CSS pixels, from the viewport's corner.
type Box struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Film is a screencast of an agent's tab.
type Film struct {
	e    *Engine
	s    *session
	cdp  string
	conn *cdpConn
	// session is the DevTools session attached to the tab; restore the size the tab had before.
	session string
	restore Size
	// Viewport is the tab's size while it is filmed.
	Viewport Size

	gone     chan struct{}
	goneOnce sync.Once
	stopped  sync.Once
}

// Done is closed when the tab or Chrome went away under the film.
func (f *Film) Done() <-chan struct{} { return f.gone }

// Film starts filming an agent's tab at viewport, or at the size it has when
// viewport is zero; frame is given each frame on the connection's goroutine,
// and must not wait there. A frame drawn while the owner holds the browser is
// not given: what the owner types is not filmed, as a phone take pauses for
// the owner's takeover (issue #817).
func (e *Engine) Film(ctx context.Context, agent string, viewport Size, frame func(Frame)) (*Film, error) {
	if !e.control.Enter() {
		return nil, ErrHeld
	}

	f, err := e.film(ctx, agent, viewport, frame)

	if e.control.Leave() {
		if f != nil {
			f.Stop(ctx)
		}

		return nil, ErrHeld
	}

	return f, err
}

func (e *Engine) film(ctx context.Context, agent string, viewport Size, frame func(Frame)) (*Film, error) {
	s, cdp, _, err := e.attach(ctx, agent)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, filmStep)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.released || !s.opened:
		return nil, ErrNoPage
	case s.filming:
		return nil, ErrFilmingTab
	}

	target, err := e.boundTarget(s)
	if err != nil {
		return nil, err
	}

	before := s.size
	if before == (Size{}) {
		before = standardPage
	}

	if viewport == (Size{}) {
		viewport = before
	}

	f := &Film{e: e, s: s, cdp: cdp, restore: before, Viewport: viewport, gone: make(chan struct{})}

	if err := e.setViewport(ctx, s, cdp, viewport); err != nil {
		return nil, err
	}

	s.filming = true

	if err := f.start(ctx, target, frame); err != nil {
		f.unfilm(ctx)

		return nil, err
	}

	return f, nil
}

// boundTarget is the tab agent-browser pinned a session to, from the file it keeps it in. The caller holds s.mu.
func (e *Engine) boundTarget(s *session) (string, error) {
	var bound struct {
		TargetID string `json:"targetId"`
	}

	raw, err := os.ReadFile(filepath.Join(e.sockets(), s.name+boundTab))
	if err != nil || json.Unmarshal(raw, &bound) != nil || bound.TargetID == "" {
		return "", ErrNoPage
	}

	return bound.TargetID, nil
}

// start attaches to the tab and starts its screencast.
func (f *Film) start(ctx context.Context, target string, frame func(Frame)) error {
	// The session is known once attachToTarget is answered, on this goroutine; the connection's own reads the
	// events meanwhile, and must not wait for it.
	var (
		mu      sync.Mutex
		session string
	)

	// The acknowledgements are sent for as long as the film lives, past the request that started it.
	life := context.WithoutCancel(ctx)

	events := func(ev cdpEvent) {
		mu.Lock()
		ours := session
		mu.Unlock()

		if ours == "" || ev.SessionID != ours && ev.Method != "Target.detachedFromTarget" {
			return
		}

		if ev.Method == "Target.detachedFromTarget" && !detached(ev.Params, ours) {
			return
		}

		switch ev.Method {
		case "Page.screencastFrame":
			f.frame(life, ev.Params, ours, frame)
		case "Target.detachedFromTarget", "Inspector.detached", "Inspector.targetCrashed":
			f.ended()
		}
	}

	conn, err := dialCDP(ctx, f.cdp, events)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	f.conn = conn

	var attached struct {
		SessionID string `json:"sessionId"`
	}

	err = conn.send(ctx, "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, "", &attached)
	if err != nil || attached.SessionID == "" {
		conn.close()

		return fmt.Errorf("%w: attaching to the tab: %w", ErrUnavailable, err)
	}

	mu.Lock()
	session, f.session = attached.SessionID, attached.SessionID
	mu.Unlock()

	go func() {
		<-conn.closed
		f.ended()
	}()

	err = conn.send(ctx, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": filmQuality, "maxWidth": f.Viewport.Width, "maxHeight": f.Viewport.Height,
		"everyNthFrame": 1,
	}, f.session, nil)
	if err != nil {
		conn.close()

		return fmt.Errorf("%w: starting the screencast: %w", ErrUnavailable, err)
	}

	return nil
}

// detached says whether Target.detachedFromTarget's params name the session.
func detached(params json.RawMessage, session string) bool {
	var got struct {
		SessionID string `json:"sessionId"`
	}

	return json.Unmarshal(params, &got) == nil && got.SessionID == session
}

// frame acknowledges a frame and gives it on, unless the owner holds the browser.
func (f *Film) frame(ctx context.Context, params json.RawMessage, session string, frame func(Frame)) {
	var got struct {
		Data      string `json:"data"`
		SessionID int64  `json:"sessionId"`
		Metadata  struct {
			Timestamp float64 `json:"timestamp"`
		} `json:"metadata"`
	}

	if json.Unmarshal(params, &got) != nil {
		return
	}

	// Acknowledged first, as autodemo does, and never waited for here: this is the connection's own goroutine.
	_, _ = f.conn.start(ctx, "Page.screencastFrameAck",
		map[string]any{"sessionId": got.SessionID}, session)

	if held, _ := f.e.control.State(); held {
		return
	}

	raw, err := base64.StdEncoding.DecodeString(got.Data)
	if err != nil || len(raw) == 0 {
		return
	}

	at := f.e.cfg.Clock.Now()
	if ts := got.Metadata.Timestamp; ts > 0 && !math.IsInf(ts, 0) {
		sec, frac := math.Modf(ts)
		at = time.Unix(int64(sec), int64(frac*1e9))
	}

	frame(Frame{JPEG: raw, At: at})
}

func (f *Film) ended() { f.goneOnce.Do(func() { close(f.gone) }) }

// Still is a picture of the tab as it is: what a take
// starts with when its page draws nothing new, as autodemo takes a screenshot
// when no frame came (src/perform.js:244-247).
func (f *Film) Still(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, filmStep)
	defer cancel()

	var shot struct {
		Data string `json:"data"`
	}

	err := f.conn.send(ctx, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": filmQuality},
		f.session, &shot)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return base64.StdEncoding.DecodeString(shot.Data)
}

// Stop ends the film and gives the tab its size back. Stopping it again does nothing.
func (f *Film) Stop(ctx context.Context) {
	f.stopped.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), filmStep)
		defer cancel()

		_ = f.conn.send(ctx, "Page.stopScreencast", map[string]any{}, f.session, nil)
		_ = f.conn.send(ctx, "Target.detachFromTarget", map[string]any{"sessionId": f.session}, "", nil)
		f.conn.close()

		f.s.mu.Lock()
		defer f.s.mu.Unlock()

		f.unfilm(ctx)
	})
}

// unfilm gives the tab back the size it had, unless it was closed. The caller holds s.mu.
func (f *Film) unfilm(ctx context.Context) {
	f.s.filming = false

	if f.s.released {
		return
	}

	if err := f.e.setViewport(ctx, f.s, f.cdp, f.restore); err != nil {
		f.e.cfg.Logger.WarnContext(ctx, "a filmed tab's size could not be set back",
			slog.String("event", "browser.film_resize_failed"), slog.String("session", f.s.name), slog.Any("err", err))
	}
}

// setViewport gives a session's tab a size, by agent-browser's own tool. The caller holds s.mu.
func (e *Engine) setViewport(ctx context.Context, s *session, cdp string, size Size) error {
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

	return nil
}

// Box is the box of an element of an agent's tab, by a selector or a ref of
// its last snapshot, as agent-browser's click finds it: its `get box`
// (cli/src/native/element.rs, get_element_bounding_box), the element's
// getBoundingClientRect in CSS pixels. A take's step rings its center (row 5).
func (e *Engine) Box(ctx context.Context, agent, selector string) (Box, error) {
	if !e.control.Enter() {
		return Box{}, ErrHeld
	}

	b, err := e.box(ctx, agent, selector)

	if e.control.Leave() {
		return Box{}, ErrHeld
	}

	return b, err
}

func (e *Engine) box(ctx context.Context, agent, selector string) (Box, error) {
	s, cdp, _, err := e.attach(ctx, agent)
	if err != nil {
		return Box{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, filmStep)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.released || !s.opened {
		return Box{}, ErrNoPage
	}

	arguments, err := json.Marshal(map[string]string{"selector": selector})
	if err != nil {
		return Box{}, err
	}

	out, err := run(ctx, command{
		Path: e.binary, Args: []string{"mcp", "call", toolBox, "-", "--tools", "all"},
		Env: e.env(s.name, cdp), Stdin: string(arguments),
	})
	if err != nil {
		return Box{}, fmt.Errorf("%w: reading the element's box: %w", ErrUnavailable, err)
	}

	var answer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError    bool `json:"isError"`
		Structured struct {
			Response struct {
				Data *Box `json:"data"`
			} `json:"response"`
		} `json:"structuredContent"`
	}

	if json.Unmarshal(out.Stdout, &answer) != nil {
		return Box{}, fmt.Errorf("%w: reading the element's box ended with code %d: %s", ErrUnavailable, out.Code,
			lastLine(string(out.Stderr)))
	}

	box := answer.Structured.Response.Data
	if answer.IsError || box == nil {
		why := "no such element"
		if len(answer.Content) > 0 && answer.Content[0].Text != "" {
			why = lastLine(answer.Content[0].Text)
		}

		return Box{}, fmt.Errorf("%w: %s: %s", ErrBadArguments, selector, why)
	}

	return *box, nil
}

// Size is the tab's viewport while it is filmed, in CSS pixels.
func (f *Film) Size() Size { return f.Viewport }
