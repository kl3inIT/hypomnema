package browser

// Tabs of the control plane's own, each in a browser context of its own
// (issue #782): the Muse driver (internal/controlplane/muse) asks one of
// muse.ai as one of the owner's Muse accounts, with that account's cookies,
// apart from the agents' profile, so that an agent's tab never carries a Muse
// session and two accounts never share cookies or a live connection. As
// www222fff/muse2api keeps a context per account
// (src/muse2api/drivers/browser/driver.py:221-250, Target.createBrowserContext
// with disposeOnDetach false, a tab made in it with Target.createTarget) and
// czg86389-hub/muse2api drives Chrome over raw DevTools (engine.py:499-525).
//
// Such a tab is no tab the owner watches or takes, and none of the owner's
// holds pauses it: a hold is one tab's (control.go, issue #1113), and what
// the owner types in a tab he holds never reaches another context. What the
// owner does in his own tab (OpenOwnerTab, ProfileCookies,
// ClearProfileCookies) is the owner's own path, as his input in a view is
// (human, view.go), and does not wait for the hold; a script the control
// plane runs in that tab (EvaluateOwnerTab) is its own step, refused while
// the owner holds his tab, as an agent's call in its own tab is (Engine.Call).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Errors of a context's tab.
var (
	// ErrHeld reports a step that did not run, or whose result was dropped,
	// because the owner holds its tab.
	ErrHeld = errors.New("interrupted by the owner, who took the tab")
	// ErrPageGone reports a tab or a Chrome that went away under a step: the
	// next Page makes a new context and tab.
	ErrPageGone = errors.New("the browser's tab went away")
)

// stepTimeout bounds one DevTools command of a context's tab.
const stepTimeout = 30 * time.Second

// Cookie is one cookie of a browser context, as DevTools has it
// (Network.Cookie and Network.CookieParam).
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// Response is the answer to one request a context's tab sent since its last navigation: its address, status and
// numeric Retry-After, none of its body (linkedin-mcp-server core/throttle.py:46-53, ThrottleHit).
type Response struct {
	URL        string
	Status     int
	RetryAfter string
}

// maxResponses bounds the responses a tab keeps between two navigations.
const maxResponses = 1000

// isolated is one browser context of the engine's own, with its connection
// and its one tab.
type isolated struct {
	// cdp is the Chrome the context was made in; a Chrome started again has none of it.
	cdp       string
	conn      *cdpConn
	contextID string
	// target and session are its tab and the DevTools session attached to it, empty while it has none.
	target  string
	session string

	// The statuses of the requests the tab sent since its last navigation, as linkedin-mcp-server records every
	// response's status on its page (core/throttle.py:67-78 and 190-210) and counts only the requests sent during
	// the call that runs (:60-64, 171-187): sent are the requests' ids since the navigation, responses their answers.
	mu        sync.Mutex
	sent      map[string]bool
	responses []Response
}

// event keeps the status of a response to a request the tab sent since its last navigation.
func (iso *isolated) event(ev cdpEvent) {
	if ev.Method != "Network.requestWillBeSent" && ev.Method != "Network.responseReceived" {
		return
	}

	var got struct {
		RequestID string `json:"requestId"`
		Response  struct {
			URL     string            `json:"url"`
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
		} `json:"response"`
	}

	if json.Unmarshal(ev.Params, &got) != nil || got.RequestID == "" {
		return
	}

	iso.mu.Lock()
	defer iso.mu.Unlock()

	if ev.SessionID != iso.session || iso.sent == nil {
		return
	}

	if ev.Method == "Network.requestWillBeSent" {
		iso.sent[got.RequestID] = true

		return
	}

	if !iso.sent[got.RequestID] || len(iso.responses) >= maxResponses {
		return
	}

	retry := ""

	for k, v := range got.Response.Headers {
		if strings.EqualFold(k, "Retry-After") {
			retry = v
		}
	}

	iso.responses = append(iso.responses, Response{URL: got.Response.URL, Status: got.Response.Status,
		RetryAfter: retry})
}

// contexts are the engine's own browser contexts by key, with a lock of
// their own: making one dials Chrome, which the engine's mu is not held for.
type contexts struct {
	mu  sync.Mutex
	all map[string]*isolated
}

// Page is the tab of a browser context of the control plane's own.
type Page struct {
	e   *Engine
	key string
	iso *isolated
}

// Page returns the tab of the context named key, made with cookies when the
// context is not there yet (or its Chrome went away), and a tab made in it
// when it has none. The cookies of a context that is already there are left
// as the context has them.
func (e *Engine) Page(ctx context.Context, key string, cookies []Cookie) (*Page, error) {
	cdp, err := e.ensure(ctx)
	if err != nil {
		return nil, err
	}

	e.isolated.mu.Lock()
	defer e.isolated.mu.Unlock()

	if e.isolated.all == nil {
		e.isolated.all = map[string]*isolated{}
	}

	iso := e.isolated.all[key]
	if iso != nil && (iso.cdp != cdp || closed(iso.conn)) {
		iso.conn.close()
		delete(e.isolated.all, key)

		iso = nil
	}

	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()

	if iso == nil {
		if iso, err = newIsolated(ctx, cdp, cookies); err != nil {
			return nil, err
		}

		e.isolated.all[key] = iso
	}

	if iso.target != "" {
		// A tab closed by itself, or by Chrome, is made again.
		err := iso.conn.send(ctx, "Target.getTargetInfo", map[string]any{"targetId": iso.target}, "", nil)
		if err != nil {
			iso.mu.Lock()
			iso.target, iso.session = "", ""
			iso.mu.Unlock()
		}
	}

	if iso.target == "" {
		if err := iso.newTab(ctx); err != nil {
			return nil, err
		}
	}

	return &Page{e: e, key: key, iso: iso}, nil
}

// ensure has Chrome running and returns its DevTools address.
func (e *Engine) ensure(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.installed(ctx); err != nil {
		return "", err
	}

	if err := e.running(ctx); err != nil {
		return "", err
	}

	e.lastUsed = e.cfg.Clock.Now()

	return e.cdp, nil
}

// newIsolated makes a browser context in the Chrome at cdp, with cookies.
func newIsolated(ctx context.Context, cdp string, cookies []Cookie) (*isolated, error) {
	iso := &isolated{cdp: cdp}

	conn, err := dialCDP(ctx, cdp, iso.event)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	iso.conn = conn

	var made struct {
		BrowserContextID string `json:"browserContextId"`
	}

	// Kept when this connection ends, as www222fff/muse2api's (driver.py:226): the context is disposed of only
	// when asked.
	err = conn.send(ctx, "Target.createBrowserContext", map[string]any{"disposeOnDetach": false}, "", &made)
	if err != nil || made.BrowserContextID == "" {
		conn.close()

		return nil, fmt.Errorf("%w: making a browser context: %w", ErrUnavailable, err)
	}

	iso.contextID = made.BrowserContextID

	if len(cookies) > 0 {
		if err := iso.setCookies(ctx, cookies); err != nil {
			_ = conn.send(ctx, "Target.disposeBrowserContext", map[string]any{"browserContextId": iso.contextID}, "", nil)
			conn.close()

			return nil, err
		}
	}

	return iso, nil
}

// newTab makes the context's tab and attaches to it.
func (iso *isolated) newTab(ctx context.Context) error {
	var created struct {
		TargetID string `json:"targetId"`
	}

	err := iso.conn.send(ctx, "Target.createTarget",
		map[string]any{"url": blankPage, "browserContextId": iso.contextID}, "", &created)
	if err != nil || created.TargetID == "" {
		return fmt.Errorf("%w: making a tab: %w", ErrUnavailable, err)
	}

	var attached struct {
		SessionID string `json:"sessionId"`
	}

	err = iso.conn.send(ctx, "Target.attachToTarget",
		map[string]any{"targetId": created.TargetID, "flatten": true}, "", &attached)
	if err != nil || attached.SessionID == "" {
		return fmt.Errorf("%w: attaching to a tab: %w", ErrUnavailable, err)
	}

	for _, domain := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if err := iso.conn.send(ctx, domain, map[string]any{}, attached.SessionID, nil); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}

	iso.mu.Lock()
	iso.target, iso.session = created.TargetID, attached.SessionID
	iso.mu.Unlock()

	return nil
}

func (iso *isolated) setCookies(ctx context.Context, cookies []Cookie) error {
	err := iso.conn.send(ctx, "Storage.setCookies",
		map[string]any{"cookies": cookies, "browserContextId": iso.contextID}, "", nil)
	if err != nil {
		return fmt.Errorf("%w: setting cookies: %w", ErrUnavailable, err)
	}

	return nil
}

// closed says whether a connection ended.
func closed(c *cdpConn) bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// DisposeContext closes the context named key and its tab; its cookies go
// with it. A key with none is left alone.
func (e *Engine) DisposeContext(ctx context.Context, key string) {
	e.isolated.mu.Lock()
	iso := e.isolated.all[key]
	delete(e.isolated.all, key)
	e.isolated.mu.Unlock()

	if iso == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()

	_ = iso.conn.send(ctx, "Target.disposeBrowserContext", map[string]any{"browserContextId": iso.contextID}, "", nil)
	iso.conn.close()
}

// step runs one DevTools command of the tab.
func (p *Page) step(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	err := p.iso.conn.send(ctx, method, params, p.iso.session, result)

	cancel()

	if err != nil {
		if errors.Is(err, errCDPClosed) || strings.Contains(err.Error(), "No session with given id") ||
			strings.Contains(err.Error(), "No target with given id") {
			return fmt.Errorf("%w: %w", ErrPageGone, err)
		}

		return err
	}

	p.e.mu.Lock()
	p.e.lastUsed = p.e.cfg.Clock.Now()
	p.e.mu.Unlock()

	return nil
}

// Navigate loads an address in the tab. The responses the tab keeps start again from it (Responses).
func (p *Page) Navigate(ctx context.Context, address string) error {
	p.iso.mu.Lock()
	p.iso.sent, p.iso.responses = map[string]bool{}, nil
	p.iso.mu.Unlock()

	return p.step(ctx, "Page.navigate", map[string]any{"url": address}, nil)
}

// Responses are the status of each request the tab sent since its last navigation, the page's own fetches among
// them, and when it was answered; none of their bodies.
func (p *Page) Responses(context.Context) ([]Response, error) {
	p.iso.mu.Lock()
	defer p.iso.mu.Unlock()

	return slices.Clone(p.iso.responses), nil
}

// Evaluate runs a script in the page, awaiting a promise it returns, and
// decodes its value into result when that is not nil.
func (p *Page) Evaluate(ctx context.Context, expression string, result any) error {
	var got evaluation

	if err := p.step(ctx, "Runtime.evaluate", evaluate(expression), &got); err != nil {
		return err
	}

	return got.decode(result)
}

// evaluation is Runtime.evaluate's answer.
type evaluation struct {
	Result struct {
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// evaluate is Runtime.evaluate's parameters for a script whose promise is
// awaited and whose value is returned.
func evaluate(expression string) map[string]any {
	return map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true}
}

// decode decodes the script's value into result when that is not nil, or
// reports the script's exception.
func (v evaluation) decode(result any) error {
	if x := v.ExceptionDetails; x != nil {
		text := x.Text
		if x.Exception != nil && x.Exception.Description != "" {
			text = x.Exception.Description
		}

		return fmt.Errorf("the page's script failed: %s", lastLine(text))
	}

	if result == nil || len(v.Result.Value) == 0 {
		return nil
	}

	return json.Unmarshal(v.Result.Value, result)
}

// InsertText types text into the focused element, as an input method does.
func (p *Page) InsertText(ctx context.Context, text string) error {
	return p.step(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}

// Click presses and releases the mouse's left button at a point of the page.
func (p *Page) Click(ctx context.Context, x, y float64) error {
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		err := p.step(ctx, "Input.dispatchMouseEvent",
			map[string]any{"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1}, nil)
		if err != nil {
			return err
		}
	}

	return nil
}

// KeyEnter presses Enter in the focused element.
func (p *Page) KeyEnter(ctx context.Context) error {
	for _, kind := range []string{"keyDown", "keyUp"} {
		params := map[string]any{
			"type": kind, "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13, "nativeVirtualKeyCode": 13,
		}
		if kind == "keyDown" {
			params["text"] = "\r"
		}

		if err := p.step(ctx, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
	}

	return nil
}

// Cookies are the context's cookies.
func (p *Page) Cookies(ctx context.Context) ([]Cookie, error) {
	var got struct {
		Cookies []Cookie `json:"cookies"`
	}

	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	err := p.iso.conn.send(ctx, "Storage.getCookies", map[string]any{"browserContextId": p.iso.contextID}, "", &got)

	cancel()

	if err != nil {
		return nil, fmt.Errorf("%w: reading cookies: %w", ErrPageGone, err)
	}

	return got.Cookies, nil
}

// OpenOwnerTab loads an http or https address in the owner's own tab, made
// when it is not there, for a sign-in the owner makes in his view of it. It
// is the owner's path: it runs while he holds his tab.
func (e *Engine) OpenOwnerTab(ctx context.Context, address string) error {
	if u, err := url.Parse(address); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("%w: not a web address", ErrBadArguments)
	}

	s, cdp, cat, err := e.attach(ctx, OwnerTab)
	if err != nil {
		return err
	}

	arguments, err := json.Marshal(map[string]string{"url": address})
	if err != nil {
		return err
	}

	if arguments, err = cat.arguments(toolOpen, arguments); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := e.tool(ctx, s, cdp, toolOpen, arguments); err != nil {
		return err
	}

	s.opened = true

	e.mu.Lock()
	s.used = e.cfg.Clock.Now()
	e.lastUsed = s.used
	e.mu.Unlock()

	return nil
}

// EvaluateOwnerTab runs a script in the owner's own tab, in the page's own
// world, as Page.Evaluate does in a context's tab: for a request the page
// must make from its own origin, such as muse.ai's sign-in check, which
// muse.ai refuses from any other client (issue #798; Muse-Chat-MCP's
// checkAuth, muse-driver.mjs:162-185, evaluates it in its page too). It is
// the control plane's step in the owner's tab, not the owner's: while the
// owner holds his tab it is refused with ErrHeld, and one that was
// running when he took it answers ErrHeld, its result dropped. With no
// owner's tab open it answers ErrNoSuchTab.
func (e *Engine) EvaluateOwnerTab(ctx context.Context, expression string, result any) error {
	hold, ok := e.holds.enter(OwnerTab)
	if !ok {
		return ErrHeld
	}

	got, err := e.evaluateOwnerTab(ctx, expression)

	if hold.Leave() {
		return ErrHeld
	}

	if err != nil {
		return err
	}

	return got.decode(result)
}

func (e *Engine) evaluateOwnerTab(ctx context.Context, expression string) (evaluation, error) {
	e.mu.Lock()
	cdp := e.cdp
	e.mu.Unlock()

	// agent-browser keeps the tab the owner's session is pinned to in a file, as Tabs reads it.
	var bound struct {
		TargetID string `json:"targetId"`
	}

	raw, err := os.ReadFile(filepath.Join(e.sockets(), sessionName(OwnerTab)+boundTab))
	if cdp == "" || err != nil || json.Unmarshal(raw, &bound) != nil || bound.TargetID == "" {
		return evaluation{}, fmt.Errorf("%w: %s", ErrNoSuchTab, OwnerTab)
	}

	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()

	// Attached on a connection of its own, as readFavicon is, and detached after.
	conn, err := dialCDP(ctx, cdp, nil)
	if err != nil {
		return evaluation{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer conn.close()

	var attached struct {
		SessionID string `json:"sessionId"`
	}

	err = conn.send(ctx, "Target.attachToTarget",
		map[string]any{"targetId": bound.TargetID, "flatten": true}, "", &attached)
	if err != nil || attached.SessionID == "" {
		return evaluation{}, fmt.Errorf("%w: %s: %w", ErrNoSuchTab, OwnerTab, err)
	}

	defer func() {
		_ = conn.send(ctx, "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, "", nil)
	}()

	var got evaluation

	if err := conn.send(ctx, "Runtime.evaluate", evaluate(expression), attached.SessionID, &got); err != nil {
		return evaluation{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return got, nil
}

// ProfileCookies are the cookies of the shared profile, the agents' and the
// owner's tab's, whose domain is domain or under it. It starts nothing: with
// Chrome not running there are none to read.
func (e *Engine) ProfileCookies(ctx context.Context, domain string) ([]Cookie, error) {
	conn, err := e.profileConn(ctx)
	if err != nil || conn == nil {
		return nil, err
	}
	defer conn.close()

	var got struct {
		Cookies []Cookie `json:"cookies"`
	}

	if err := conn.send(ctx, "Storage.getCookies", map[string]any{}, "", &got); err != nil {
		return nil, fmt.Errorf("%w: reading cookies: %w", ErrUnavailable, err)
	}

	var out []Cookie

	for _, c := range got.Cookies {
		if inDomain(c.Domain, domain) {
			out = append(out, c)
		}
	}

	return out, nil
}

// ClearProfileCookies removes cookies from the shared profile: each is set
// again as one that expired, which Chrome takes as its removal.
func (e *Engine) ClearProfileCookies(ctx context.Context, cookies []Cookie) error {
	if len(cookies) == 0 {
		return nil
	}

	conn, err := e.profileConn(ctx)
	if err != nil || conn == nil {
		return err
	}
	defer conn.close()

	expired := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		c.Value, c.Expires = "", 1
		expired = append(expired, c)
	}

	if err := conn.send(ctx, "Storage.setCookies", map[string]any{"cookies": expired}, "", nil); err != nil {
		return fmt.Errorf("%w: clearing cookies: %w", ErrUnavailable, err)
	}

	return nil
}

// profileConn is a connection to the running Chrome, nil when it is not
// running.
func (e *Engine) profileConn(ctx context.Context) (*cdpConn, error) {
	e.mu.Lock()
	cdp := e.cdp
	e.mu.Unlock()

	if cdp == "" {
		return nil, nil //nolint:nilnil // No Chrome: nothing to read or clear.
	}

	conn, err := dialCDP(ctx, cdp, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return conn, nil
}

// inDomain says whether a cookie's domain is domain or under it.
func inDomain(cookie, domain string) bool {
	cookie = strings.TrimPrefix(cookie, ".")

	return cookie == domain || strings.HasSuffix(cookie, "."+domain)
}
