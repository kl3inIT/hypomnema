package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The owner watching a tab (issue #219). Each agent-browser session serves
// its tab on a loopback WebSocket (cli/src/native/stream/): frames as base64
// JPEG, one on the way until it is acknowledged (`pacing=ack`), and input as
// `input_mouse` and `input_keyboard`. A View connects to it for the owner and
// puts the control gate in front of its input, which agent-browser itself
// does not have. What the page sends and is sent follows T3 Code's preview
// stream (apps/server/src/preview/ServerBrowser.ts), as OpenMausBot's does.

const (
	// OwnerTab is the owner's own tab, where a sign-in is made. It is made the
	// first time it is watched.
	OwnerTab = "owner"
	// maxViews is how many views may be open (OpenMausBot: four a profile).
	maxViews = 4
	// streamQuality is the frames' JPEG quality (T3 Code and OpenMausBot: 70).
	streamQuality = "70"
	// maxStreamMessage bounds one message of a session's stream: a frame.
	maxStreamMessage = 64 << 20
	// streamFile ends the name of the file in which a session's daemon writes its stream's port.
	streamFile = ".stream"
)

// The controller a view is told.
const (
	ControllerAgent = "agent"
	ControllerYou   = "you"
)

var (
	// ErrNoSuchTab reports a tab that is not open.
	ErrNoSuchTab = errors.New("no such tab")
	// ErrTooManyViews reports that as many views are open as may be.
	ErrTooManyViews = errors.New("too many browser views are open; close one first")
)

// Size is a tab's size in CSS pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Update is what a view has for the page; each part is there or empty.
type Update struct {
	// JPEG is a frame. The page acknowledges it before the next one comes.
	JPEG []byte
	// Viewport is the tab's size, to map the pointer by.
	Viewport *Size
	// URL is the tab's address.
	URL string
	// Controller is who drives the browser, ControllerAgent or ControllerYou.
	Controller string
}

// Action is one message of the page, in T3 Code's names: `ack`,
// `takeControl`, `releaseControl`, `resize`, `mouse`, `wheel`, `key`,
// `navigate`, `history`, `reload`.
type Action struct {
	Type string `json:"type"`
	// Action is `move`, `down` or `up` for the mouse and `down` or `up` for a key.
	Action     string  `json:"action"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button"`
	ClickCount int     `json:"clickCount"`
	// Modifiers are Chrome's bits: 1 Alt, 2 Ctrl, 4 Meta, 8 Shift.
	Modifiers int     `json:"modifiers"`
	DeltaX    float64 `json:"deltaX"`
	DeltaY    float64 `json:"deltaY"`
	Key       string  `json:"key"`
	Code      string  `json:"code"`
	Text      string  `json:"text"`
	KeyCode   int     `json:"keyCode"`
	URL       string  `json:"url"`
	// Delta is -1 for back and 1 for forward.
	Delta int `json:"delta"`
	// Width and Height are the pane's size in CSS pixels, for `resize`.
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// streamMessage is what a session's stream sends that a view uses.
type streamMessage struct {
	Type     string `json:"type"`
	Seq      uint64 `json:"seq"`
	Data     string `json:"data"`
	URL      string `json:"url"`
	Metadata struct {
		DeviceWidth  int `json:"deviceWidth"`
		DeviceHeight int `json:"deviceHeight"`
	} `json:"metadata"`
	ViewportWidth  int `json:"viewportWidth"`
	ViewportHeight int `json:"viewportHeight"`
	Tabs           []struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	} `json:"tabs"`
}

// View is the owner's view of one tab.
type View struct {
	e    *Engine
	s    *session
	tab  string
	conn *websocket.Conn
	stop context.CancelFunc

	messages chan streamMessage
	// again asks Next to say who controls once more: an input was dropped.
	again chan struct{}

	// What follows is Next's own, and seq also Do's.
	mu       sync.Mutex
	seq      uint64
	told     bool
	held     bool
	viewport Size
	url      string

	closing sync.Once
}

// Watch opens the owner's view of a tab: an agent's, by the name the agent
// has in the browser, or OwnerTab.
func (e *Engine) Watch(ctx context.Context, tab string) (*View, error) {
	if !e.control.opened(maxViews) {
		return nil, ErrTooManyViews
	}

	v, err := e.watch(ctx, tab)
	if err != nil {
		e.control.unopened()

		return nil, err
	}

	return v, nil
}

func (e *Engine) watch(ctx context.Context, tab string) (*View, error) {
	s, err := e.viewed(ctx, tab)
	if err != nil {
		return nil, err
	}

	port, err := os.ReadFile(filepath.Join(e.sockets(), s.name+streamFile))
	if err != nil {
		e.unviewed(s)

		return nil, fmt.Errorf("%w: the tab has no stream: %w", ErrUnavailable, err)
	}

	// The stream outlives the request that opened it.
	streaming, stop := context.WithCancel(context.WithoutCancel(ctx))

	// No Origin is sent, which agent-browser takes from a program on this machine.
	//nolint:bodyclose // A WebSocket's response has no body left to close.
	conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:"+strings.TrimSpace(string(port))+"/?pacing=ack", nil)
	if err != nil {
		stop()
		e.unviewed(s)

		return nil, fmt.Errorf("%w: reaching the tab's stream: %w", ErrUnavailable, err)
	}

	conn.SetReadLimit(maxStreamMessage)

	v := &View{
		e: e, s: s, tab: tab, conn: conn, stop: stop,
		messages: make(chan streamMessage, 8), again: make(chan struct{}, 1),
	}

	go v.pump(streaming)

	return v, nil
}

// viewed returns the tab's session with a view counted on it, so that it is
// not closed as idle. The owner's own tab is made when it is not there.
func (e *Engine) viewed(ctx context.Context, tab string) (*session, error) {
	if tab == OwnerTab {
		s, cdp, _, err := e.attach(ctx, OwnerTab)
		if err != nil {
			return nil, err
		}

		s.mu.Lock()
		defer s.mu.Unlock()

		if !s.opened {
			if _, err := e.tool(ctx, s, cdp, toolOpen, json.RawMessage(`{"url":"`+blankPage+`"}`)); err != nil {
				return nil, err
			}

			s.opened = true
		}

		e.mu.Lock()
		defer e.mu.Unlock()

		s.views++

		return s, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.sessions[tab]
	if !ok || e.cdp == "" {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchTab, tab)
	}

	s.views++

	return s, nil
}

func (e *Engine) unviewed(s *session) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s.views--
	s.used = e.cfg.Clock.Now()
}

// pump reads the session's stream until it ends, keeping what a view uses.
func (v *View) pump(ctx context.Context) {
	defer close(v.messages)

	for {
		_, raw, err := v.conn.Read(ctx)
		if err != nil {
			return
		}

		var m streamMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}

		switch m.Type {
		case "frame", "status", "url", "tabs":
			select {
			case v.messages <- m:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Next waits for what the page is to be given next. It fails when the tab's
// stream ended.
func (v *View) Next(ctx context.Context) (Update, error) {
	for {
		held, changed := v.e.control.state()

		v.mu.Lock()
		say := !v.told || held != v.held
		v.told, v.held = true, held
		v.mu.Unlock()

		if say {
			if held {
				return Update{Controller: ControllerYou}, nil
			}

			return Update{Controller: ControllerAgent}, nil
		}

		select {
		case m, ok := <-v.messages:
			if !ok {
				return Update{}, fmt.Errorf("%w: the tab's stream ended", ErrNoSuchTab)
			}

			if up, ok := v.update(m); ok {
				return up, nil
			}
		case <-changed:
		case <-v.again:
			v.mu.Lock()
			v.told = false
			v.mu.Unlock()
		case <-ctx.Done():
			return Update{}, ctx.Err()
		}
	}
}

// update is what a stream's message gives the page, if anything new.
func (v *View) update(m streamMessage) (Update, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()

	var up Update

	size := Size{Width: m.ViewportWidth, Height: m.ViewportHeight}
	if m.Type == "frame" {
		size = Size{Width: m.Metadata.DeviceWidth, Height: m.Metadata.DeviceHeight}
	}

	if size.Width > 0 && size.Height > 0 && size != v.viewport {
		v.viewport = size
		up.Viewport = &size
	}

	address := m.URL

	for _, tab := range m.Tabs {
		if tab.Active {
			address = tab.URL
		}
	}

	if address != "" && address != v.url {
		v.url = address
		up.URL = address
	}

	if m.Type == "frame" {
		jpeg, err := base64.StdEncoding.DecodeString(m.Data)
		if err != nil {
			return Update{}, false
		}

		v.seq = m.Seq
		up.JPEG = jpeg
	}

	return up, up.JPEG != nil || up.Viewport != nil || up.URL != ""
}

// Do carries out one message of the page. Input and navigation need control:
// without it they are dropped and the page is told again who controls.
func (v *View) Do(ctx context.Context, a Action) error {
	now := v.e.cfg.Clock.Now()

	switch a.Type {
	case "ack":
		v.mu.Lock()
		seq := v.seq
		v.mu.Unlock()

		return v.send(ctx, map[string]any{"type": "ack", "seq": seq})
	case "takeControl":
		v.e.control.take(ctx, now)
		v.e.cfg.Logger.InfoContext(ctx, "the owner took the browser",
			slog.String("event", "browser.taken"), slog.String("tab", v.tab))

		return nil
	case "releaseControl":
		if v.e.control.release() {
			v.e.released(ctx, releasedAsked)
		}

		return nil
	case "resize":
		// The tab fills the pane whoever drives (fill.go): its size is the page's
		// shape, at least a desktop wide, which an agent's work does not depend on.
		size, ok := fillPage(a.Width, a.Height)
		if !ok {
			return nil
		}

		// A size not set leaves the tab scaled into the pane, as before: the view goes on.
		if err := v.e.resize(ctx, v.s, size); err != nil {
			v.e.cfg.Logger.WarnContext(ctx, "a tab's size could not be set",
				slog.String("event", "browser.resize_failed"), slog.String("tab", v.tab), slog.Any("err", err))
		}

		return nil
	}

	message, tool, arguments := input(a)
	if message == nil && tool == "" {
		return nil
	}

	if !v.e.control.input(now) {
		select {
		case v.again <- struct{}{}:
		default:
		}

		return nil
	}

	if message != nil {
		return v.send(ctx, message)
	}

	return v.e.human(ctx, v.s, tool, arguments)
}

func (v *View) send(ctx context.Context, message map[string]any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}

	return v.conn.Write(ctx, websocket.MessageText, raw)
}

// input is what the owner's action is to agent-browser: a message of its
// stream, or a tool of the session with its arguments.
func input(a Action) (message map[string]any, tool string, arguments json.RawMessage) {
	switch a.Type {
	case "mouse":
		event, ok := map[string]string{"move": "mouseMoved", "down": "mousePressed", "up": "mouseReleased"}[a.Action]
		if !ok {
			return nil, "", nil
		}

		button := a.Button
		if button == "" {
			button = "none"
		}

		return map[string]any{
			"type": "input_mouse", "eventType": event, "x": a.X, "y": a.Y, "button": button,
			"clickCount": a.ClickCount, "modifiers": a.Modifiers,
		}, "", nil
	case "wheel":
		return map[string]any{
			"type": "input_mouse", "eventType": "mouseWheel", "x": a.X, "y": a.Y,
			"deltaX": a.DeltaX, "deltaY": a.DeltaY, "modifiers": a.Modifiers,
		}, "", nil
	case "key":
		message := map[string]any{
			"type": "input_keyboard", "eventType": "keyUp", "windowsVirtualKeyCode": a.KeyCode,
			"modifiers": a.Modifiers,
		}

		if a.Key != "" {
			message["key"] = a.Key
		}

		if a.Code != "" {
			message["code"] = a.Code
		}

		switch {
		case a.Action == "up":
		case a.Action != "down":
			return nil, "", nil
		case a.Text != "":
			// A key that types: Chrome inserts the text with the key down.
			message["eventType"], message["text"] = "keyDown", a.Text
		default:
			message["eventType"] = "rawKeyDown"
		}

		return message, "", nil
	case "navigate":
		raw, err := json.Marshal(map[string]string{"url": a.URL})
		if err != nil {
			return nil, "", nil
		}

		return nil, toolOpen, raw
	case "history":
		if a.Delta < 0 {
			return nil, "agent_browser_back", nil
		}

		return nil, "agent_browser_forward", nil
	case "reload":
		return nil, "agent_browser_reload", nil
	}

	return nil, "", nil
}

// human runs a tool in a tab for the owner, who holds the browser.
func (e *Engine) human(ctx context.Context, s *session, tool string, arguments json.RawMessage) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	e.mu.Lock()
	cdp, cat := e.cdp, e.catalog
	e.mu.Unlock()

	if cat == nil || cdp == "" {
		return ErrUnavailable
	}

	arguments, err := cat.arguments(tool, arguments)
	if err != nil {
		// An address that is not one is the owner's slip, not the view's end.
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = e.tool(ctx, s, cdp, tool, arguments)

	return err
}

// Close ends the view. clean says the owner closed it, which gives the
// browser back at once; a view that dropped keeps the hold for a while.
func (v *View) Close(ctx context.Context, clean bool) {
	v.closing.Do(func() {
		v.stop()
		_ = v.conn.Close(websocket.StatusNormalClosure, "")

		v.e.unviewed(v.s)
		v.e.unwatched(ctx, v.s)

		if reason := v.e.control.closed(clean, v.e.cfg.Clock.Now()); reason != "" {
			v.e.released(ctx, reason)
		}
	})
}

func (e *Engine) released(ctx context.Context, reason string) {
	e.cfg.Logger.InfoContext(ctx, "the browser went back to the agents",
		slog.String("event", "browser.released"), slog.String("reason", reason))
}

// Tab is one tab of the shared browser.
type Tab struct {
	// ID is the name its agent has in the browser, or OwnerTab.
	ID    string
	URL   string
	Title string
	// Views is how many views of it are open.
	Views int
}

// Tabs lists the open tabs, and says whether the owner holds the browser.
// It starts nothing.
func (e *Engine) Tabs(ctx context.Context) ([]Tab, bool) {
	held, _ := e.control.state()

	e.mu.Lock()

	if e.cdp == "" {
		e.mu.Unlock()

		return nil, held
	}

	cdp := e.cdp
	tabs := make([]Tab, 0, len(e.sessions))
	targets := map[string]int{}

	for agent, s := range e.sessions {
		tab := Tab{ID: agent, Views: s.views}

		// agent-browser keeps the tab a session is pinned to, and its last address, in a file.
		var bound struct {
			TargetID string `json:"targetId"`
			URL      string `json:"url"`
		}

		if raw, err := os.ReadFile(filepath.Join(e.sockets(), s.name+boundTab)); err == nil &&
			json.Unmarshal(raw, &bound) == nil {
			tab.URL = bound.URL
			targets[bound.TargetID] = len(tabs)
		}

		tabs = append(tabs, tab)
	}

	e.mu.Unlock()

	// The titles, and the addresses as they are now, are Chrome's own list of its tabs. agent-browser's
	// `tab list` keeps the title a tab had when its daemon first saw it (tried on zm, 2026-11-12).
	for _, target := range chromeTabs(ctx, cdp) {
		if i, ok := targets[target.ID]; ok {
			tabs[i].URL, tabs[i].Title = target.URL, target.Title
		}
	}

	return tabs, held
}

// chromeTab is one of Chrome's targets as its DevTools port lists them.
type chromeTab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// chromeTabs asks Chrome's DevTools port, on loopback, for its tabs; none
// when it does not answer.
func chromeTabs(ctx context.Context, cdp string) []chromeTab {
	address, err := url.Parse(cdp)
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address.Host+"/json/list", nil)
	if err != nil {
		return nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed.

	var listed []chromeTab
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&listed) != nil {
		return nil
	}

	return listed
}

// streamPorts are the ports the sessions' streams listen on.
func (e *Engine) streamPorts() []uint16 {
	files, _ := filepath.Glob(filepath.Join(e.sockets(), "*"+streamFile))

	ports := make([]uint16, 0, len(files))

	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // agent-browser's own file, under the engine's directory.
		if err != nil {
			continue
		}

		if port, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 16); err == nil {
			ports = append(ports, uint16(port))
		}
	}

	return ports
}
