package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/ownerhold"
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
		"Page.enable", "Runtime.enable", "Network.enable", "Runtime.evaluate", "Input.insertText",
		"Input.dispatchMouseEvent", "Input.dispatchMouseEvent", "Input.dispatchKeyEvent", "Input.dispatchKeyEvent",
	}, chrome.methods("S1"))
	assert.Contains(t, strings.Join(sent, "\n"), `Storage.getCookies {"browserContextId":"C1"}`)

	e.DisposeContext(t.Context(), "acc_1")
	assert.Contains(t, chrome.sent()[len(chrome.sent())-1], `Target.disposeBrowserContext {"browserContextId":"C1"}`)
}

// A tab keeps the status of each request it sent since its last navigation, the page's own fetches among them, as
// linkedin-mcp-server's listener does (core/throttle.py): not a request sent before the navigation, nor another
// tab's (issue #1291, review round 1 of PR #1359).
func TestATabKeepsTheStatusesOfItsOwnRequestsSinceItsNavigation(t *testing.T) {
	t.Parallel()

	chrome := contextChrome()
	e := runningEngine(chrome.serve(t))

	p, err := e.Page(t.Context(), "acc_1", nil)
	require.NoError(t, err)

	event := func(session, method, params string) {
		p.iso.event(cdpEvent{Method: method, SessionID: session, Params: []byte(params)})
	}

	event("S1", "Network.requestWillBeSent", `{"requestId":"old"}`)
	require.NoError(t, p.Navigate(t.Context(), "https://www.linkedin.com/search/results/content/?keywords=a"))

	event("S1", "Network.responseReceived", `{"requestId":"old","response":{"url":"https://x/old","status":429}}`)
	event("S1", "Network.requestWillBeSent", `{"requestId":"doc"}`)
	event("S1", "Network.requestWillBeSent", `{"requestId":"xhr"}`)
	event("S1", "Network.responseReceived",
		`{"requestId":"doc","response":{"url":"https://www.linkedin.com/search","status":200}}`)
	event("S1", "Network.responseReceived",
		`{"requestId":"xhr","response":{"url":"https://www.linkedin.com/rsc","status":999,"headers":{"retry-after":"60"}}}`)
	event("S9", "Network.requestWillBeSent", `{"requestId":"other"}`)
	event("S9", "Network.responseReceived", `{"requestId":"other","response":{"url":"https://y","status":429}}`)

	got, err := p.Responses(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []Response{
		{URL: "https://www.linkedin.com/search", Status: 200},
		{URL: "https://www.linkedin.com/rsc", Status: 999, RetryAfter: "60"},
	}, got)

	require.NoError(t, p.Navigate(t.Context(), "https://www.linkedin.com/search/results/content/?keywords=b"))
	got, err = p.Responses(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got, "a navigation starts the record again")
	// Past the record's bound, ordinary answers are dropped and a refusal is still kept (review round 2 of PR #1359).
	for i := range maxResponses + 5 {
		id := fmt.Sprint("r", i)
		event("S1", "Network.requestWillBeSent", `{"requestId":"`+id+`"}`)
		event("S1", "Network.responseReceived", `{"requestId":"`+id+`","response":{"url":"https://a","status":200}}`)
	}

	event("S1", "Network.requestWillBeSent", `{"requestId":"late"}`)
	event("S1", "Network.responseReceived",
		`{"requestId":"late","response":{"url":"https://www.linkedin.com/r","status":999}}`)

	got, err = p.Responses(t.Context())
	require.NoError(t, err)
	assert.Len(t, got, maxResponses+1)
	assert.Equal(t, 999, got[len(got)-1].Status)
}

// holdOf is a tab's hold, the one its views take.
func holdOf(e *Engine, tab string) *ownerhold.Hold {
	e.holds.mu.Lock()
	defer e.holds.mu.Unlock()

	return e.holds.of(tab)
}

// A context's tab is no tab the owner takes: holding his own tab, or an
// agent's, leaves its steps running (issue #1113), as a hold is one tab's.
func TestAContextsStepsRunWhileTheOwnerHoldsATab(t *testing.T) {
	t.Parallel()

	chrome := contextChrome()
	e := runningEngine(chrome.serve(t))

	holdOf(e, OwnerTab).Take(t.Context(), time.Now())
	holdOf(e, "d-one").Take(t.Context(), time.Now())

	p, err := e.Page(t.Context(), "acc_1", nil)
	require.NoError(t, err)

	var state map[string]any

	require.NoError(t, p.Evaluate(t.Context(), "1", &state))
	require.NoError(t, p.InsertText(t.Context(), "x"))
	_, err = p.Cookies(t.Context())
	require.NoError(t, err)
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
	holdOf(e, OwnerTab).Take(t.Context(), time.Now())

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

// A script runs in the owner's own tab, attached to it on a connection of its
// own, only while the owner does not hold that tab; with no owner's tab it
// answers ErrNoSuchTab.
func TestAScriptRunsInTheOwnersTabUnlessHeld(t *testing.T) {
	t.Parallel()

	chrome := contextChrome()
	inner := chrome.answer
	chrome.answer = func(method, session string) (string, []string) {
		if method == "Runtime.evaluate" {
			return `{"result":{"type":"object","value":{"status":200,"ok":true,"viewer":"42"}}}`, nil
		}

		return inner(method, session)
	}

	e := runningEngine(chrome.serve(t))
	e.cfg.Dir = t.TempDir()

	var got struct {
		Status int    `json:"status"`
		Viewer string `json:"viewer"`
	}

	require.ErrorIs(t, e.EvaluateOwnerTab(t.Context(), "1", &got), ErrNoSuchTab, "no owner's tab yet")
	assert.Empty(t, chrome.sent(), "nothing is asked of Chrome without the tab")

	require.NoError(t, os.MkdirAll(e.sockets(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(e.sockets(), sessionName(OwnerTab)+boundTab),
		[]byte(`{"targetId":"OWN","url":"https://muse.ai/"}`), 0o600))

	require.NoError(t, e.EvaluateOwnerTab(t.Context(), "check()", &got))
	assert.Equal(t, 200, got.Status)
	assert.Equal(t, "42", got.Viewer)

	sent := chrome.sent()
	require.Len(t, sent, 3)
	assert.Contains(t, sent[0], `Target.attachToTarget {"flatten":true,"targetId":"OWN"}`)
	assert.Contains(t, sent[1], `S1 Runtime.evaluate {"awaitPromise":true,"expression":"check()","returnByValue":true}`)
	assert.Contains(t, sent[2], `Target.detachFromTarget {"sessionId":"S1"}`)

	holdOf(e, "d-one").Take(t.Context(), time.Now())
	require.NoError(t, e.EvaluateOwnerTab(t.Context(), "check()", &got), "an agent's tab held is not the owner's")

	holdOf(e, OwnerTab).Take(t.Context(), time.Now())

	got.Status = 0

	require.ErrorIs(t, e.EvaluateOwnerTab(t.Context(), "check()", &got), ErrHeld)
	assert.Zero(t, got.Status)
	assert.Len(t, chrome.sent(), 6, "nothing is sent while the owner holds his tab")

	require.True(t, holdOf(e, OwnerTab).Release())
	require.NoError(t, e.EvaluateOwnerTab(t.Context(), "check()", &got))
}
