package browser

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runningEngine is an Engine that takes the Chrome at cdp as running, with
// nothing to install.
func runningEngine(cdp string) *Engine {
	e := New(Config{})
	e.catalog = &catalog{}
	e.cdp, e.cleared, e.checked = cdp, true, time.Now()

	return e
}

func contextChrome() *fakeChrome {
	return &fakeChrome{answer: func(method, _ string) (string, []string) {
		switch method {
		case "Target.createBrowserContext":
			return `{"browserContextId":"C1"}`, nil
		case "Target.createTarget":
			return `{"targetId":"T1"}`, nil
		case "Target.attachToTarget":
			return `{"sessionId":"S1"}`, nil
		case "Runtime.evaluate":
			return `{"result":{"type":"object","value":{"path":"/thread/new"}}}`, nil
		case "Storage.getCookies":
			return `{"cookies":[{"name":"hatch_sess","value":"v2","domain":".muse.ai"},` +
				`{"name":"c_user","value":"1","domain":".facebook.com"}]}`, nil
		}

		return `{}`, nil
	}}
}

// An account's tab is made in a browser context of its own, with its cookies,
// and every step goes on that tab's session; the same key reuses them.
func TestAContextIsMadeWithItsCookiesAndItsTab(t *testing.T) {
	t.Parallel()

	chrome := contextChrome()
	e := runningEngine(chrome.serve(t))

	jar := []Cookie{{Name: "hatch_sess", Value: "v1", Domain: ".muse.ai", Path: "/", Secure: true}}

	p, err := e.Page(t.Context(), "acc_1", jar)
	require.NoError(t, err)

	var state struct {
		Path string `json:"path"`
	}

	require.NoError(t, p.Evaluate(t.Context(), "location.pathname", &state))
	assert.Equal(t, "/thread/new", state.Path)
	require.NoError(t, p.InsertText(t.Context(), "hello"))
	require.NoError(t, p.Click(t.Context(), 10, 20))
	require.NoError(t, p.KeyEnter(t.Context()))

	cookies, err := p.Cookies(t.Context())
	require.NoError(t, err)
	assert.Len(t, cookies, 2)

	again, err := e.Page(t.Context(), "acc_1", nil)
	require.NoError(t, err)
	assert.Same(t, p.iso, again.iso, "the same key keeps its context")

	sent := chrome.sent()
	assert.Contains(t, sent[0], `Target.createBrowserContext {"disposeOnDetach":false}`)
	assert.Contains(t, sent[1], `Storage.setCookies {"browserContextId":"C1","cookies":[{"name":"hatch_sess","value":"v1"`)
	assert.Contains(t, sent[2], `Target.createTarget {"browserContextId":"C1","url":"about:blank"}`)
	assert.Equal(t, []string{
		"Page.enable", "Runtime.enable", "Runtime.evaluate", "Input.insertText",
		"Input.dispatchMouseEvent", "Input.dispatchMouseEvent", "Input.dispatchKeyEvent", "Input.dispatchKeyEvent",
	}, chrome.methods("S1"))
	assert.Contains(t, strings.Join(sent, "\n"), `Storage.getCookies {"browserContextId":"C1"}`)

	e.DisposeContext(t.Context(), "acc_1")
	assert.Contains(t, chrome.sent()[len(chrome.sent())-1], `Target.disposeBrowserContext {"browserContextId":"C1"}`)
}

// While the owner holds the browser no step runs; a step running when he
// takes it answers ErrHeld, what it read dropped.
func TestTheOwnersHoldStopsAContextsSteps(t *testing.T) {
	t.Parallel()

	var e *Engine

	var taking atomic.Bool

	chrome := contextChrome()
	inner := chrome.answer
	chrome.answer = func(method, session string) (string, []string) {
		if method == "Runtime.evaluate" && taking.Load() {
			// The owner takes the browser while the page answers.
			go e.control.Take(t.Context(), time.Now())

			for held, _ := e.control.State(); !held; held, _ = e.control.State() {
				time.Sleep(time.Millisecond)
			}
		}

		return inner(method, session)
	}

	e = runningEngine(chrome.serve(t))

	p, err := e.Page(t.Context(), "acc_1", nil)
	require.NoError(t, err)

	taking.Store(true)

	var state map[string]any

	require.ErrorIs(t, p.Evaluate(t.Context(), "1", &state), ErrHeld)
	assert.Nil(t, state, "what the step read is dropped")

	before := len(chrome.sent())

	require.ErrorIs(t, p.InsertText(t.Context(), "x"), ErrHeld)
	_, err = p.Cookies(t.Context())
	require.ErrorIs(t, err, ErrHeld)
	_, err = e.Page(t.Context(), "acc_2", nil)
	require.ErrorIs(t, err, ErrHeld)
	assert.Len(t, chrome.sent(), before, "nothing is sent while the owner holds the browser")

	require.True(t, e.control.Release())
	require.NoError(t, p.InsertText(t.Context(), "x"))
}

// A tab that went away is made again in the same context; a Chrome that went
// away takes its contexts with it, and the next Page makes a new one.
func TestAGoneTabOrChromeIsMadeAgain(t *testing.T) {
	t.Parallel()

	var gone atomic.Bool

	chrome := contextChrome()
	inner := chrome.answer
	chrome.answer = func(method, session string) (string, []string) {
		if method == "Target.getTargetInfo" && gone.Load() {
			return "!No target with given id found", nil
		}

		return inner(method, session)
	}

	e := runningEngine(chrome.serve(t))

	first, err := e.Page(t.Context(), "acc_1", nil)
	require.NoError(t, err)

	gone.Store(true)

	second, err := e.Page(t.Context(), "acc_1", nil)
	require.NoError(t, err)
	assert.Same(t, first.iso, second.iso)
	assert.Equal(t, 2, strings.Count(strings.Join(chrome.sent(), "\n"), "Target.createTarget"))

	// Chrome started again: another address.
	other := contextChrome()

	e.mu.Lock()
	e.cdp = other.serve(t)
	e.mu.Unlock()

	third, err := e.Page(t.Context(), "acc_1", []Cookie{{Name: "hatch_sess", Value: "v3", Domain: ".muse.ai"}})
	require.NoError(t, err)
	assert.NotSame(t, first.iso, third.iso)
	assert.Contains(t, other.sent()[1], `"value":"v3"`, "the new context is made with the cookies given")

	// A step on the old one finds its connection closed.
	first.iso.conn.close()
	require.ErrorIs(t, first.InsertText(t.Context(), "x"), ErrPageGone)
}

// The shared profile's cookies are read by domain and cleared by being set
// again expired; the owner's path does not wait for his hold.
func TestTheProfilesCookiesAreReadAndClearedByDomain(t *testing.T) {
	t.Parallel()

	chrome := contextChrome()
	e := runningEngine(chrome.serve(t))
	e.control.Take(t.Context(), time.Now())

	cookies, err := e.ProfileCookies(t.Context(), "muse.ai")
	require.NoError(t, err)
	require.Len(t, cookies, 1)
	assert.Equal(t, "hatch_sess", cookies[0].Name)

	require.NoError(t, e.ClearProfileCookies(t.Context(), cookies))
	sent := chrome.sent()
	assert.Contains(t, sent[len(sent)-1],
		`Storage.setCookies {"cookies":[{"name":"hatch_sess","value":"","domain":".muse.ai","expires":1}]}`)

	assert.True(t, inDomain("muse.ai", "muse.ai"))
	assert.True(t, inDomain(".www.muse.ai", "muse.ai"))
	assert.False(t, inDomain("notmuse.ai", "muse.ai"))

	none := New(Config{})
	cookies, err = none.ProfileCookies(t.Context(), "muse.ai")
	require.NoError(t, err)
	assert.Empty(t, cookies, "no Chrome, no cookies")
}
