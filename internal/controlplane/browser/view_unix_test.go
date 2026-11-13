//go:build unix

package browser

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	t.Cleanup(func() { v.Close(context.WithoutCancel(t.Context()), false) })

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

// Scenario: the owner signs in. The owner's own tab is made when it is first
// watched. From "Take control" to "Release" no agent's call runs, a read
// included: each is answered that the browser is paused, and nothing reaches
// agent-browser. After the release the agents go on.
func TestWhileTheOwnerHoldsTheBrowserNoAgentUsesIt(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, OwnerTab)
	v := r.watch(t, OwnerTab)

	made := containing(r.runs(t), "session=owner", "mcp call agent_browser_open")
	require.Len(t, made, 1, "the owner's tab is made for the view")

	assert.Equal(t, ControllerAgent, next(t, v).Controller)

	do(t, v, `{"type":"takeControl"}`)
	assert.Equal(t, ControllerYou, next(t, v).Controller)
	assert.True(t, r.held(t))

	for _, tool := range []string{"agent_browser_get_text", "agent_browser_click"} {
		result, err := r.Call(t.Context(), "d-one", tool, nil)
		require.NoError(t, err)
		assert.JSONEq(t, `{"content":[{"type":"text","text":"`+Refusal+`"}],"isError":true}`, string(result))
	}

	assert.Empty(t, containing(r.runs(t), "session=d-one"), "a refused call does not reach agent-browser")

	do(t, v, `{"type":"releaseControl"}`)
	assert.Equal(t, ControllerAgent, next(t, v).Controller)

	result, err := r.Call(t.Context(), "d-one", "agent_browser_get_text", nil)
	require.NoError(t, err)
	assert.False(t, isRefusal(t, result))
}

// A call that was running when the owner took the browser ends, and is then
// answered as refused: what it read may be what the owner typed. Taking
// control waits for it.
func TestACallRunningWhenTheOwnerTakesTheBrowserIsAnsweredAsRefused(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.stream(t, OwnerTab)
	v := r.watch(t, OwnerTab)
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

// The browser goes back to the agents at once when the owner closes the
// view, HoldGrace after a view that dropped, and HoldIdle after the owner's
// last input.
func TestTheOwnersHoldEndsByItself(t *testing.T) {
	r := newRig(t, Config{})
	st := r.stream(t, OwnerTab)

	closed := r.watch(t, OwnerTab)
	do(t, closed, `{"type":"takeControl"}`)
	closed.Close(t.Context(), true)
	assert.False(t, r.held(t), "a view the owner closed gives the browser back at once")

	dropped := r.watch(t, OwnerTab)
	do(t, dropped, `{"type":"takeControl"}`)
	dropped.Close(t.Context(), false)

	r.clock.pass(HoldGrace - time.Second)
	r.Sweep(t.Context())
	assert.True(t, r.held(t), "a view that dropped may come back")

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
// stream is asked for one frame at a time; a frame reaches the page as a
// JPEG with the tab's size and address, and the page's acknowledgement lets
// the next one come. The owner's mouse, keys and navigation go in only while
// the owner holds the browser; without that they are dropped and the page is
// told again who controls.
func TestAViewShowsATabAndTakesInputOnlyWithControl(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	st := r.stream(t, "d-one")
	v := r.watch(t, "d-one")

	assert.Equal(t, "pacing=ack", <-st.asked)
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

	do(t, v, `{"type":"ack"}`)
	assert.JSONEq(t, `{"type":"ack","seq":7}`, <-st.got)

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
		v.Close(t.Context(), true)
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

	v.Close(t.Context(), true)

	sizes := containing(r.runs(t), "viewport session=d-one")
	require.Len(t, sizes, 3, "the agent drives, and the same pane sets nothing again")
	assert.Contains(t, sizes[0], `{"height":900,"width":1600}`)
	assert.Contains(t, sizes[1], `{"height":2297,"width":1280}`, "a phone's pane gives a desktop's width")
	assert.Contains(t, sizes[2], `{"height":720,"width":1280}`, "nobody watches")
}
