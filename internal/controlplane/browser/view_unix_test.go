//go:build unix

package browser

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stream stands for the stream of a session's tab, which agent-browser's
// daemon serves on a loopback port named in a file beside its socket. It
// keeps how it was asked, sends what `send` is given and keeps what it gets.
type stream struct {
	asked chan string
	send  chan string
	got   chan string
}

// stream puts a stand-in stream where the session's daemon would have put
// its own.
func (r rig) stream(t *testing.T, session string) *stream {
	t.Helper()

	st := &stream{asked: make(chan string, 8), send: make(chan string, 8), got: make(chan string, 8)}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck // A test's connection.

		st.asked <- req.URL.RawQuery

		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()

		go func() {
			for {
				select {
				case message := <-st.send:
					_ = conn.Write(ctx, websocket.MessageText, []byte(message))
				case <-ctx.Done():
					return
				}
			}
		}()

		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}

			st.got <- string(data)
		}
	}))
	t.Cleanup(srv.Close)

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(r.sockets(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.sockets(), session+streamFile), []byte(port), 0o600))

	return st
}

// watch opens a view of a tab and ends it with the test.
func (r rig) watch(t *testing.T, tab string) *View {
	t.Helper()

	v, err := r.Watch(t.Context(), tab)
	require.NoError(t, err)
	t.Cleanup(func() { v.Close(context.WithoutCancel(t.Context())) })

	return v
}

func next(t *testing.T, v *View) Update {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	up, err := v.Next(ctx)
	require.NoError(t, err)

	return up
}

func do(t *testing.T, v *View, action string) {
	t.Helper()

	var a Action
	require.NoError(t, json.Unmarshal([]byte(action), &a))
	require.NoError(t, v.Do(t.Context(), a))
}

func (r rig) held(t *testing.T) bool {
	t.Helper()

	_, held := r.Tabs(t.Context())

	return held
}

func isRefusal(t *testing.T, result json.RawMessage) bool {
	t.Helper()

	return string(result) == string(refusal())
}

// Scenario: the owner helps an agent past a sign-in in its tab. The owner's
// own tab is made when it is first watched. From "Take control" to "Release"
// none of the agent's calls in the tab held runs, a read included: each is
// answered that its tab is paused, and nothing reaches agent-browser. After
// the release the agent goes on.
func TestWhileTheOwnerHoldsATabItsAgentDoesNotUseIt(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, OwnerTab)
	r.watch(t, OwnerTab)

	made := containing(r.runs(t), "session=owner", "mcp call agent_browser_open")
	require.Len(t, made, 1, "the owner's tab is made for the view")

	r.stream(t, "d-one")
	v := r.watch(t, "d-one")
	assert.Equal(t, ControllerAgent, next(t, v).Controller)

	do(t, v, `{"type":"takeControl"}`)
	assert.Equal(t, ControllerYou, next(t, v).Controller)
	assert.True(t, r.held(t))

	for _, tool := range []string{"agent_browser_get_text", "agent_browser_click"} {
		result, err := r.Call(t.Context(), "d-one", tool, nil)
		require.NoError(t, err)
		assert.JSONEq(t, `{"content":[{"type":"text","text":"`+Refusal+`"}],"isError":true}`, string(result))
	}

	assert.Empty(t, containing(r.runs(t), "session=d-one", "mcp call"), "a refused call does not reach agent-browser")

	do(t, v, `{"type":"releaseControl"}`)
	assert.Equal(t, ControllerAgent, next(t, v).Controller)
	assert.False(t, r.held(t))

	result, err := r.Call(t.Context(), "d-one", "agent_browser_get_text", nil)
	require.NoError(t, err)
	assert.False(t, isRefusal(t, result))
}

// A call that was running when the owner took its tab ends, and is then
// answered as refused: what it read may be what the owner typed. Taking
// control waits for it.
func TestACallRunningWhenTheOwnerTakesItsTabIsAnsweredAsRefused(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, "d-one")
	v := r.watch(t, "d-one")
	r.runs(t)
	r.touch(t, "slow", "")

	answered := make(chan json.RawMessage, 1)

	go func() {
		result, _ := r.Call(t.Context(), "d-one", "agent_browser_get_text", nil)
		answered <- result
	}()

	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(r.bin, "log"))

		return len(containing([]string{string(raw)}, "session=d-one", "agent_browser_get_text")) == 1
	}, 10*time.Second, 10*time.Millisecond, "the agent's call is running")

	// The call takes a second from when it started.
	asked := time.Now()

	do(t, v, `{"type":"takeControl"}`)
	assert.Greater(t, time.Since(asked), 300*time.Millisecond, "taking control waits for the agent's running call")

	select {
	case result := <-answered:
		assert.True(t, isRefusal(t, result))
	case <-time.After(10 * time.Second):
		t.Fatal("the agent's call was not answered")
	}
}

// A tab goes back to its agent at once when the owner hands it back,
// HoldGrace after the last view went, closed by the page or dropped (issue
// #603), and HoldIdle after the owner's last input.
func TestTheOwnersHoldEndsByItself(t *testing.T) {
	r := newRig(t, Config{})
	st := r.stream(t, OwnerTab)

	handed := r.watch(t, OwnerTab)
	do(t, handed, `{"type":"takeControl"}`)
	do(t, handed, `{"type":"releaseControl"}`)
	assert.False(t, r.held(t), "handing back gives the tab back at once")
	handed.Close(t.Context())

	closed := r.watch(t, OwnerTab)
	do(t, closed, `{"type":"takeControl"}`)
	closed.Close(t.Context())

	r.clock.pass(HoldGrace - time.Second)
	r.Sweep(t.Context())
	assert.True(t, r.held(t), "a view the page closed keeps the hold for the grace")

	r.clock.pass(time.Second)
	r.Sweep(t.Context())
	assert.False(t, r.held(t))

	idle := r.watch(t, OwnerTab)
	do(t, idle, `{"type":"takeControl"}`)

	r.clock.pass(HoldIdle - time.Second)
	do(t, idle, `{"type":"mouse","action":"move","x":1,"y":1}`)
	assert.Contains(t, <-st.got, "mouseMoved")

	r.clock.pass(HoldIdle - time.Second)
	r.Sweep(t.Context())
	assert.True(t, r.held(t), "input keeps the hold")

	r.clock.pass(time.Second)
	r.Sweep(t.Context())
	assert.False(t, r.held(t))
}

// Scenario: the owner watches an agent's tab and helps it past a page. The
// stream is asked for frames that wait to be acknowledged, at most 15 a
// second; a frame reaches the page as a JPEG with the tab's size and address.
// The owner's mouse, keys and navigation go in only while
// the owner holds the tab; without that they are dropped and the page is
// told again who controls.
func TestAViewShowsATabAndTakesInputOnlyWithControl(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	st := r.stream(t, "d-one")
	v := r.watch(t, "d-one")

	assert.Equal(t, "pacing=ack&maxFps=15", <-st.asked)
	assert.Equal(t, ControllerAgent, next(t, v).Controller)

	port, err := strconv.ParseUint(readFile(t, filepath.Join(r.sockets(), "d-one"+streamFile)), 10, 16)
	require.NoError(t, err)
	assert.True(t, r.ownPort(uint16(port)), "a page in the browser never reaches a tab's stream")

	st.send <- `{"type":"status","viewportWidth":1280,"viewportHeight":720}`
	assert.Equal(t, &Size{Width: 1280, Height: 720}, next(t, v).Viewport)

	st.send <- `{"type":"command","action":"click"}`
	st.send <- `{"type":"url","url":"https://example.com/"}`
	assert.Equal(t, "https://example.com/", next(t, v).URL, "what the page has no use for is not passed on")

	st.send <- `{"type":"frame","seq":7,"data":"YSBmcmFtZQ==","metadata":{"deviceWidth":1280,"deviceHeight":720}}`
	frame := next(t, v)
	assert.Equal(t, "a frame", string(frame.JPEG))
	assert.Nil(t, frame.Viewport, "the size is said when it changes")
	assert.JSONEq(t, `{"type":"ack","seq":7}`, <-st.got)
	do(t, v, `{"type":"ack"}`)

	// Without control: dropped, and the page is told who controls.
	do(t, v, `{"type":"mouse","action":"down","x":5,"y":6,"button":"left","clickCount":1}`)
	do(t, v, `{"type":"navigate","url":"https://example.org"}`)
	assert.Equal(t, ControllerAgent, next(t, v).Controller)

	do(t, v, `{"type":"takeControl"}`)
	assert.Equal(t, ControllerYou, next(t, v).Controller)
	r.runs(t)

	do(t, v, `{"type":"mouse","action":"down","x":5,"y":6,"button":"left","clickCount":1,"modifiers":8}`)
	assert.JSONEq(t, `{"type":"input_mouse","eventType":"mousePressed","x":5,"y":6,"button":"left","clickCount":1,
		"modifiers":8}`, <-st.got, "the mouse dropped before control was taken never reached the tab")

	do(t, v, `{"type":"wheel","x":5,"y":6,"deltaY":120}`)
	assert.JSONEq(t, `{"type":"input_mouse","eventType":"mouseWheel","x":5,"y":6,"deltaX":0,"deltaY":120,
		"modifiers":0}`, <-st.got)

	do(t, v, `{"type":"key","action":"down","key":"a","code":"KeyA","text":"a","keyCode":65}`)
	assert.JSONEq(t, `{"type":"input_keyboard","eventType":"keyDown","key":"a","code":"KeyA","text":"a",
		"windowsVirtualKeyCode":65,"modifiers":0}`, <-st.got)

	do(t, v, `{"type":"key","action":"down","key":"Enter","code":"Enter","keyCode":13}`)
	assert.JSONEq(t, `{"type":"input_keyboard","eventType":"rawKeyDown","key":"Enter","code":"Enter",
		"windowsVirtualKeyCode":13,"modifiers":0}`, <-st.got)

	do(t, v, `{"type":"key","action":"up","key":"Enter","code":"Enter","keyCode":13}`)
	assert.Contains(t, <-st.got, `"eventType":"keyUp"`)

	do(t, v, `{"type":"navigate","url":"example.org/login"}`)
	do(t, v, `{"type":"navigate","url":"file:///etc/passwd"}`)
	do(t, v, `{"type":"history","delta":-1}`)
	do(t, v, `{"type":"reload"}`)

	runs := containing(r.runs(t), "session=d-one", "mcp call")
	require.Len(t, runs, 3, "an address that is not a web address is dropped")
	assert.Contains(t, runs[0], "args=mcp call agent_browser_open")
	assert.Contains(t, runs[1], "args=mcp call agent_browser_back")
	assert.Contains(t, runs[2], "args=mcp call agent_browser_reload")
}

// The page may have two frames on their way unacknowledged, never more: the
// view acknowledges agent-browser's frame itself while the page has fewer,
// and otherwise once the page acknowledges its oldest (issue #540).
func TestAViewKeepsTwoFramesOnTheWayToThePage(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	st := r.stream(t, "d-one")
	v := r.watch(t, "d-one")
	assert.Equal(t, ControllerAgent, next(t, v).Controller)

	frame := func(seq string) {
		t.Helper()

		st.send <- `{"type":"frame","seq":` + seq +
			`,"data":"YSBmcmFtZQ==","metadata":{"deviceWidth":1280,"deviceHeight":720}}`
		assert.Equal(t, "a frame", string(next(t, v).JPEG))
	}

	acked := func() string {
		t.Helper()

		select {
		case got := <-st.got:
			return got
		case <-time.After(300 * time.Millisecond):
			return ""
		}
	}

	frame("1")
	assert.JSONEq(t, `{"type":"ack","seq":1}`, acked(), "one on its way: the next may come")

	frame("2")
	assert.Empty(t, acked(), "two on their way: the next waits")

	do(t, v, `{"type":"ack"}`)
	assert.JSONEq(t, `{"type":"ack","seq":2}`, acked(), "the page took the first: the next may come")

	frame("3")
	assert.Empty(t, acked())

	do(t, v, `{"type":"ack"}`)
	assert.JSONEq(t, `{"type":"ack","seq":3}`, acked())

	do(t, v, `{"type":"ack"}`)
	do(t, v, `{"type":"ack"}`)
	assert.Empty(t, acked(), "nothing waits: the page's acknowledgements go no further")

	frame("4")
	assert.JSONEq(t, `{"type":"ack","seq":4}`, acked())
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path) //nolint:gosec // The test's own directory.
	require.NoError(t, err)

	return string(raw)
}

// Only an open tab is watched, by so many views at a time; and a tab that
// is watched is in use, however long its agent has been away.
func TestOnlyOpenTabsAreWatchedAndAWatchedTabIsKept(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Watch(t.Context(), "d-one")
	require.ErrorIs(t, err, ErrNoSuchTab, "nothing is started for a tab that is not there")
	assert.False(t, r.Status().Running)

	_, err = r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, "d-one")

	views := make([]*View, 0, maxViews)
	for range maxViews {
		views = append(views, r.watch(t, "d-one"))
	}

	_, err = r.Watch(t.Context(), "d-one")
	require.ErrorIs(t, err, ErrTooManyViews)

	r.clock.pass(TabIdle + time.Minute)
	r.Sweep(t.Context())
	assert.Equal(t, []string{"d-one"}, r.Status().Agents, "a tab that is watched is not closed as idle")

	for _, v := range views {
		v.Close(t.Context())
	}

	r.clock.pass(TabIdle)
	r.Sweep(t.Context())
	assert.Empty(t, r.Status().Agents, "the tab is idle again from when its last view closed")
}

// The tabs are listed with whose each is, its address and its title as
// Chrome has them now, and how many views it has.
func TestTabsListsEachTabWithItsAddressAndTitle(t *testing.T) {
	r := newRig(t, Config{})

	tabs, held := r.Tabs(t.Context())
	assert.Empty(t, tabs, "a browser that is not running has no tab, and is not started to be asked")
	assert.False(t, held)
	assert.Empty(t, r.runs(t))

	// Chrome's own list of its tabs, on its DevTools port.
	chrome := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/json/list" {
			http.NotFound(w, req)

			return
		}

		_, _ = io.WriteString(w, `[{"id":"T-d-one","type":"page","url":"https://example.com/","title":"Example Domain"},`+
			`{"id":"T-host","type":"page","url":"about:blank","title":""}]`)
	}))
	t.Cleanup(chrome.Close)

	_, port, err := net.SplitHostPort(chrome.Listener.Addr().String())
	require.NoError(t, err)
	r.touch(t, "devtools", port)

	_, err = r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, OwnerTab)
	r.watch(t, OwnerTab)

	tabs, _ = r.Tabs(t.Context())
	assert.ElementsMatch(t, []Tab{
		{ID: "d-one", URL: "https://example.com/", Title: "Example Domain"},
		{ID: OwnerTab, URL: "about:blank", Views: 1},
	}, tabs)
}

// The tab fills the pane it is watched in, whoever drives, at least a desktop
// wide (issue #193); a pane that has not changed, or is out of bounds, sets
// nothing, and the tab nobody watches has agent-browser's size back.
func TestATabFillsThePaneItIsWatchedIn(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, "d-one")
	v, err := r.Watch(t.Context(), "d-one")
	require.NoError(t, err)
	assert.Equal(t, ControllerAgent, next(t, v).Controller)
	r.runs(t)

	do(t, v, `{"type":"resize","width":1600,"height":900}`)
	do(t, v, `{"type":"resize","width":1600,"height":900}`)
	do(t, v, `{"type":"resize","width":390,"height":700}`)
	do(t, v, `{"type":"resize","width":0,"height":0}`)

	v.Close(t.Context())

	sizes := containing(r.runs(t), "viewport session=d-one")
	require.Len(t, sizes, 3, "the agent drives, and the same pane sets nothing again")
	assert.Contains(t, sizes[0], `{"height":900,"width":1600}`)
	assert.Contains(t, sizes[1], `{"height":2297,"width":1280}`, "a phone's pane gives a desktop's width")
	assert.Contains(t, sizes[2], `{"height":720,"width":1280}`, "nobody watches")
}

// Scenario (issue #1113): the owner opens a Bot's chat, signs in to a site in his own tab and takes control there,
// then looks at the Bot's tab. Only the tab taken is held: the Bot's calls in its own tab go on, and its tab's view
// says the Bot drives it, while the owner's tab's view says he does; taking the Bot's tab holds that one too.
func TestTakingControlHoldsOnlyTheTabTaken(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "b-cht_1", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, OwnerTab)
	r.stream(t, sessionName("b-cht_1"))

	mine := r.watch(t, OwnerTab)
	assert.Equal(t, ControllerAgent, next(t, mine).Controller)

	do(t, mine, `{"type":"takeControl"}`)
	assert.Equal(t, ControllerYou, next(t, mine).Controller)

	bots := r.watch(t, "b-cht_1")
	assert.Equal(t, ControllerAgent, next(t, bots).Controller, "the Bot's tab is not the one held")

	result, err := r.Call(t.Context(), "b-cht_1", "agent_browser_get_text", nil)
	require.NoError(t, err)
	assert.False(t, isRefusal(t, result), "the Bot's tools go on in its own tab")

	do(t, bots, `{"type":"takeControl"}`)
	assert.Equal(t, ControllerYou, next(t, bots).Controller)

	result, err = r.Call(t.Context(), "b-cht_1", "agent_browser_get_text", nil)
	require.NoError(t, err)
	assert.True(t, isRefusal(t, result), "the Bot's tab is held once it is taken")

	do(t, mine, `{"type":"releaseControl"}`)
	assert.Equal(t, ControllerAgent, next(t, mine).Controller)

	result, err = r.Call(t.Context(), "b-cht_1", "agent_browser_get_text", nil)
	require.NoError(t, err)
	assert.True(t, isRefusal(t, result), "handing back the owner's tab leaves the Bot's held")
}

// Chrome paints only the tab in front of its window, and every tab is in one window: a view brings its tab to the
// front as it opens, as agent-browser's own tab switch does (Page.bringToFront), or the tab's stream sends no frame
// once another tab came in front, the owner's made for his view say (issue #1113).
func TestAViewBringsItsTabToTheFront(t *testing.T) {
	r, chrome := frontRig(t)

	r.watch(t, OwnerTab)
	require.Eventually(t, func() bool { return len(chrome.activated()) == 1 }, 10*time.Second, 10*time.Millisecond)
	r.watch(t, "b-cht_1")
	require.Eventually(t, func() bool { return len(chrome.activated()) == 2 }, 10*time.Second, 10*time.Millisecond)

	assert.Equal(t, []string{front(OwnerTab), front("b-cht_1")}, chrome.activated())
}

// frontRig is a rig whose Chrome is a fake that keeps what it is asked, with the Bot's tab of chat cht_1 open and a
// stream for it and for the owner's tab.
func frontRig(t *testing.T) (rig, *activations) {
	t.Helper()

	r := newRig(t, Config{})

	chrome := &activations{&fakeChrome{answer: func(string, string) (string, []string) { return `{}`, nil }}}
	address, err := url.Parse(chrome.serve(t))
	require.NoError(t, err)
	r.touch(t, "devtools", address.Port())

	_, err = r.Call(t.Context(), "b-cht_1", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, OwnerTab)
	r.stream(t, sessionName("b-cht_1"))

	return r, chrome
}

type activations struct{ *fakeChrome }

// activated are the tabs Chrome was asked to bring to the front, in order.
func (c *activations) activated() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var got []string

	for _, command := range c.got {
		if strings.HasPrefix(command, "Target.activateTarget") {
			got = append(got, command)
		}
	}

	return got
}

// front is the command that brings an agent's tab to the front.
func front(agent string) string {
	return `Target.activateTarget {"targetId":"T-` + sessionName(agent) + `"}`
}

// Review of PR #1233: a tab is brought to the front as the owner's action, queued behind the agents' calls running in
// the browser, as OpenMausBot's viewer brings a tab to the front through its control (server/browser/viewers/core.ts,
// tabSelect: control.human(...showTab)): moving the focus away from a Bot's tab mid-call failed a wait that needs the
// focus (agent_browser_wait_for_text, LOST_FOCUS). Opening the view does not wait for it, and the call is answered
// before the tab moves.
func TestReviewOpeningOwnerViewWhileBotActs(t *testing.T) {
	r, chrome := frontRig(t)

	first := r.watch(t, OwnerTab)
	require.Eventually(t, func() bool { return len(chrome.activated()) == 1 }, 10*time.Second, 10*time.Millisecond)
	first.Close(t.Context())

	r.touch(t, "slow", "")

	answered := make(chan json.RawMessage, 1)

	go func() {
		result, _ := r.Call(t.Context(), "b-cht_1", "agent_browser_wait_for_text", nil)
		answered <- result
	}()

	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(r.bin, "log"))

		return strings.Contains(string(raw), "agent_browser_wait_for_text")
	}, 10*time.Second, 10*time.Millisecond, "the Bot's call is running")

	r.watch(t, OwnerTab)
	time.Sleep(300 * time.Millisecond)

	select {
	case <-answered:
		t.Fatal("the call takes a second; the view opened while it ran")
	default:
	}

	assert.Len(t, chrome.activated(), 1, "the owner's tab is not brought to the front while the Bot's call runs")

	select {
	case result := <-answered:
		assert.False(t, isRefusal(t, result))
	case <-time.After(10 * time.Second):
		t.Fatal("the Bot's call was not answered")
	}

	require.Eventually(t, func() bool { return len(chrome.activated()) == 2 }, 10*time.Second, 10*time.Millisecond,
		"the owner's tab comes to the front once the call ended")
	assert.Equal(t, front(OwnerTab), chrome.activated()[1])
}

// Review of PR #1233: the owner holds his tab in one pane, watches the Bot's in another, and closes that pane. The
// view still open is the only one left, and its tab goes back to the front, where Chrome paints it.
func TestReviewHeldViewAfterOtherPaneCloses(t *testing.T) {
	r, chrome := frontRig(t)

	mine := r.watch(t, OwnerTab)
	do(t, mine, `{"type":"takeControl"}`)
	require.Eventually(t, func() bool { return len(chrome.activated()) == 1 }, 10*time.Second, 10*time.Millisecond)

	bots := r.watch(t, "b-cht_1")
	require.Eventually(t, func() bool { return len(chrome.activated()) == 2 }, 10*time.Second, 10*time.Millisecond)

	bots.Close(t.Context())

	require.Eventually(t, func() bool { return len(chrome.activated()) == 3 }, 10*time.Second, 10*time.Millisecond,
		"the view left brings its tab to the front again")
	assert.Equal(t, []string{front(OwnerTab), front("b-cht_1"), front(OwnerTab)}, chrome.activated())
}

// slowCall starts an agent's call of a second (the stand-in's `slow`) and returns where its answer comes, once the
// call is running.
func (r rig) slowCall(t *testing.T, agent, tool string) <-chan json.RawMessage {
	t.Helper()

	r.touch(t, "slow", "")

	answered := make(chan json.RawMessage, 1)

	go func() {
		result, _ := r.Call(t.Context(), agent, tool, nil)
		answered <- result
	}()

	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(r.bin, "log"))

		return len(containing(strings.Split(string(raw), "\n"), "session="+sessionName(agent), tool)) > 0
	}, 10*time.Second, 10*time.Millisecond, "the call is running")

	return answered
}

func answer(t *testing.T, answered <-chan json.RawMessage) json.RawMessage {
	t.Helper()

	select {
	case result := <-answered:
		return result
	case <-time.After(20 * time.Second):
		t.Fatal("the call was not answered")

		return nil
	}
}

// Review round 2 of PR #1233: Bot A's call runs, and the owner opens Bot B's tab, whose move to the front waits for
// it. Bot B calls meanwhile, its call queued behind the move, and the owner takes Bot B's tab before it runs. The
// hold is checked when a call is about to run, not only when it is asked for: Bot B's call is refused and never
// reaches agent-browser, and taking control does not wait for it.
func TestReviewTakeWhileACallWaitsForTheFront(t *testing.T) {
	r, chrome := frontRig(t)

	_, err := r.Call(t.Context(), "b-cht_2", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)
	r.stream(t, sessionName("b-cht_2"))

	a := r.slowCall(t, "b-cht_1", "agent_browser_wait_for_text")

	v := r.watch(t, "b-cht_2")
	require.Eventually(t, func() bool {
		r.focus.mu.Lock()
		defer r.focus.mu.Unlock()

		return r.focus.queued != nil
	}, 10*time.Second, time.Millisecond, "the move to Bot B's tab is queued behind Bot A's call")

	b := make(chan json.RawMessage, 1)

	go func() {
		result, _ := r.Call(t.Context(), "b-cht_2", "agent_browser_click", json.RawMessage(`{"selector":"#go"}`))
		b <- result
	}()

	time.Sleep(200 * time.Millisecond)

	asked := time.Now()

	do(t, v, `{"type":"takeControl"}`)
	assert.Less(t, time.Since(asked), 500*time.Millisecond, "a call not yet running is not waited for")
	assert.Equal(t, ControllerYou, next(t, v).Controller)

	assert.False(t, isRefusal(t, answer(t, a)), "Bot A's own tab is not held")
	assert.True(t, isRefusal(t, answer(t, b)))
	assert.Empty(t, containing(r.runs(t), "session="+sessionName("b-cht_2"), "agent_browser_click"),
		"Bot B's call never reached its tab once the owner held it")
	require.Eventually(t, func() bool { return len(chrome.activated()) > 0 }, 10*time.Second, 10*time.Millisecond)
}

// Review round 2 of PR #1233: with Bot A's call running, the owner opens his tab and then Bot B's, both views open.
// The moves replace each other: once the call ended, only the newest view's tab is brought to the front.
func TestReviewOnlyTheNewestViewComesForward(t *testing.T) {
	r, chrome := frontRig(t)

	_, err := r.Call(t.Context(), "b-cht_2", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)
	r.stream(t, sessionName("b-cht_2"))

	first := r.watch(t, OwnerTab)
	require.Eventually(t, func() bool { return len(chrome.activated()) == 1 }, 10*time.Second, 10*time.Millisecond)

	a := r.slowCall(t, "b-cht_1", "agent_browser_wait_for_text")

	r.watch(t, "b-cht_1")
	r.watch(t, "b-cht_2")

	assert.False(t, isRefusal(t, answer(t, a)))
	require.Eventually(t, func() bool { return len(chrome.activated()) >= 2 }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	assert.Equal(t, []string{front(OwnerTab), front("b-cht_2")}, chrome.activated(),
		"the move to Bot A's tab, replaced, is not made")

	first.Close(t.Context())
}

// Review round 2 of PR #1233: the owner's tab is made the first time it is watched, and a tab that is made comes in
// front of the others (agent-browser brings a new tab to the front). Making it is queued behind the agents' calls
// running, as bringing a tab to the front is.
func TestReviewMakingTheOwnersTabWaitsForRunningCalls(t *testing.T) {
	r, _ := frontRig(t)

	a := r.slowCall(t, "b-cht_1", "agent_browser_wait_for_text")

	opened := make(chan error, 1)

	go func() {
		v, err := r.Watch(t.Context(), OwnerTab)
		if err == nil {
			t.Cleanup(func() { v.Close(context.WithoutCancel(t.Context())) })
		}
		opened <- err
	}()

	time.Sleep(300 * time.Millisecond)

	raw, err := os.ReadFile(filepath.Join(r.bin, "log"))
	require.NoError(t, err)
	assert.Empty(t, containing(strings.Split(string(raw), "\n"), "session=owner", "agent_browser_open"),
		"the owner's tab is not made while the Bot's call runs")

	assert.False(t, isRefusal(t, answer(t, a)))
	require.NoError(t, <-opened)

	raw, err = os.ReadFile(filepath.Join(r.bin, "log"))
	require.NoError(t, err)
	assert.NotEmpty(t, containing(strings.Split(string(raw), "\n"), "session=owner", "agent_browser_open"))
}

// Review round 3 of PR #1233: the owner's request is received before Bot B's,
// but making the owner's tab takes the first request's wait. Bot B's later
// request still gets the focus after the owner's tab is made.
func TestReviewALaterViewRequestKeepsItsTurnAfterTheOwnersTabIsMade(t *testing.T) {
	r, chrome := frontRig(t)
	r.focus.wait = 500 * time.Millisecond

	_, err := r.Call(t.Context(), "b-cht_2", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)
	r.stream(t, sessionName("b-cht_2"))

	a := r.slowCall(t, "b-cht_1", "agent_browser_wait_for_text")

	opened := make(chan error, 1)
	go func() {
		v, err := r.Watch(t.Context(), OwnerTab)
		if err == nil {
			t.Cleanup(func() { v.Close(context.WithoutCancel(t.Context())) })
		}
		opened <- err
	}()

	require.Eventually(t, func() bool {
		r.focus.mu.Lock()
		defer r.focus.mu.Unlock()

		return r.focus.queued != nil
	}, 10*time.Second, time.Millisecond, "the owner's tab is waiting to be made")
	b := r.watch(t, "b-cht_2")

	assert.False(t, isRefusal(t, answer(t, a)))
	require.NoError(t, <-opened)

	require.Eventually(t, func() bool { return len(chrome.activated()) > 0 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{front("b-cht_2")}, chrome.activated(), "the later request keeps the focus")

	b.Close(t.Context())
	require.Eventually(t, func() bool { return len(chrome.activated()) >= 2 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{front("b-cht_2"), front(OwnerTab)}, chrome.activated(),
		"closing the later pane restores the earlier one")
}
