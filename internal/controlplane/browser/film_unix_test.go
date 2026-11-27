//go:build unix

package browser

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// filmStandIn is a program put where agent-browser is: it writes each run's arguments and input to `log` beside
// itself and answers `get box` with the box in `box`, or as agent-browser answers an element it cannot find.
const filmStandIn = `#!/bin/sh
dir=$(dirname "$0")
in=$(cat)
echo "$AGENT_BROWSER_SESSION $* $in" >> "$dir/log"
if [ "$3" = agent_browser_get_box ]; then
  if [ -f "$dir/box" ]; then
    printf '{"content":[{"type":"text","text":"box"}],"isError":false,'
    printf '"structuredContent":{"response":{"success":true,"data":%s}}}\n' "$(cat "$dir/box")"
    exit 0
  fi
  printf '{"content":[{"type":"text","text":"Element not found: #nothing"}],"isError":true,'
  echo '"structuredContent":{"response":{"success":false}}}'
  exit 1
fi
echo '{"content":[{"type":"text","text":"done"}],"isError":false}'
`

// filmedEngine is an engine with Chrome at cdp, agent-browser a stand-in in dir, and agent's tab open on T1.
func filmedEngine(t *testing.T, cdp, agent string) (*Engine, string) {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-browser")
	// Write in a child shell so a parallel test's fork cannot inherit a writable descriptor and cause ETXTBSY.
	//nolint:gosec // The test's own shell, writing into its own folder.
	write := exec.CommandContext(t.Context(), "/bin/sh", "-c", `cat > "$1" && chmod 700 "$1"`, "sh", bin)
	write.Stdin = strings.NewReader(filmStandIn)
	out, err := write.CombinedOutput()
	require.NoError(t, err, "%s", out)

	e := runningEngine(cdp)
	e.cfg.Dir, e.binary = dir, bin

	s := &session{name: sessionName(agent), opened: true, used: time.Now()}
	e.sessions[agent] = s

	require.NoError(t, os.MkdirAll(e.sockets(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(e.sockets(), s.name+boundTab),
		[]byte(`{"targetId":"T1","url":"https://shop.example/"}`), 0o600))

	return e, filepath.Join(dir, "log")
}

func logged(t *testing.T, log string) string {
	t.Helper()

	raw, err := os.ReadFile(log) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	return string(raw)
}

// A take films the agent's own tab: agent-browser gives it the take's viewport, Chrome's screencast is started on
// it in JPEG at quality 90 as large as that, each frame is acknowledged and given with
// when Chrome drew it, but not while the owner holds the browser; the owner's view does not resize the tab while it
// films, and the tab gets its size back when it stops.
func TestAFilmIsTheScreencastOfTheAgentsTab(t *testing.T) {
	t.Parallel()

	jpeg := base64.StdEncoding.EncodeToString([]byte("a JPEG"))
	frame := func(n int, ts string) string {
		return `{"method":"Page.screencastFrame","sessionId":"S1","params":{"data":"` + jpeg + `","sessionId":` +
			strconv.Itoa(n) + `,"metadata":{"timestamp":` + ts + `}}}`
	}

	chrome := &fakeChrome{push: make(chan string, 4), answer: func(method, _ string) (string, []string) {
		switch method {
		case "Target.attachToTarget":
			return `{"sessionId":"S1"}`, nil
		case "Page.startScreencast":
			return `{}`, []string{frame(1, "1795683600.25")}
		}

		return `{}`, nil
	}}

	e, log := filmedEngine(t, chrome.serve(t), "d-1")
	got := make(chan Frame, 4)

	f, err := e.Film(t.Context(), "d-1", Size{Width: 390, Height: 844}, func(fr Frame) { got <- fr })
	require.NoError(t, err)
	assert.Equal(t, Size{Width: 390, Height: 844}, f.Size())

	first := <-got
	assert.Equal(t, []byte("a JPEG"), first.JPEG)
	assert.Equal(t, time.Date(2026, 11, 26, 9, 0, 0, 250_000_000, time.UTC), first.At.UTC())

	sent := strings.Join(chrome.sent(), "\n")
	assert.Contains(t, sent, `Target.attachToTarget {"flatten":true,"targetId":"T1"}`)
	assert.Contains(t, sent,
		`S1 Page.startScreencast {"everyNthFrame":1,"format":"jpeg","maxHeight":844,"maxWidth":390,"quality":90}`)
	assert.Contains(t, logged(t, log),
		`d-1 mcp call agent_browser_set_viewport - --tools mobile {"height":844,"width":390}`)

	require.Eventually(t, func() bool {
		return strings.Contains(strings.Join(chrome.sent(), "\n"), `S1 Page.screencastFrameAck {"sessionId":1}`)
	}, 5*time.Second, 10*time.Millisecond)

	// While the owner holds the tab, a frame is acknowledged and not given; his own tab held is not the agent's.
	holdOf(e, OwnerTab).Take(t.Context(), time.Now())
	chrome.push <- frame(5, "1795683600.5")

	second := <-got
	assert.Equal(t, []byte("a JPEG"), second.JPEG, "the owner's own tab held leaves the agent's filmed")

	holdOf(e, "d-1").Take(t.Context(), time.Now())
	chrome.push <- frame(2, "1795683601")

	require.Eventually(t, func() bool {
		return strings.Contains(strings.Join(chrome.sent(), "\n"), `S1 Page.screencastFrameAck {"sessionId":2}`)
	}, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, got, "what the owner does is not filmed")

	_, err = f.Still(t.Context())
	require.ErrorIs(t, err, ErrHeld, "no still is taken while the owner holds the tab")

	require.Eventually(t, func() bool { return f.drawnInHold(time.Now()) }, 5*time.Second, time.Millisecond)
	inHold := time.Now()
	holdOf(e, "d-1").Release()

	// A frame Chrome drew during the hold that comes after "Hand back" is not given either; one drawn after it is.
	require.Eventually(t, func() bool { return !f.drawnInHold(time.Now()) }, 5*time.Second, time.Millisecond)
	unix := func(at time.Time) string { return strconv.FormatFloat(float64(at.UnixNano())/1e9, 'f', 6, 64) }
	chrome.push <- frame(3, unix(inHold))
	chrome.push <- frame(4, unix(time.Now().Add(time.Second)))

	after := <-got
	assert.True(t, after.At.After(inHold), "the frame drawn during the hold was dropped")

	// The owner's view does not resize a filmed tab.
	require.NoError(t, e.resize(t.Context(), e.sessions["d-1"], Size{Width: 1600, Height: 900}))
	assert.NotContains(t, logged(t, log), `"width":1600`)

	_, err = e.Film(t.Context(), "d-1", Size{}, func(Frame) {})
	require.ErrorIs(t, err, ErrFilmingTab)

	f.Stop(t.Context())
	<-f.Done()

	sent = strings.Join(chrome.sent(), "\n")
	assert.Contains(t, sent, "S1 Page.stopScreencast")
	assert.Contains(t, sent, `Target.detachFromTarget {"sessionId":"S1"}`)
	assert.Contains(t, logged(t, log), `{"height":720,"width":1280}`, "the tab's size before it was filmed")
}

// A tab that went away ends the film; an agent with no tab of its own has nothing to film.
func TestAFilmEndsWithItsTab(t *testing.T) {
	t.Parallel()

	chrome := &fakeChrome{push: make(chan string, 1), answer: func(method, _ string) (string, []string) {
		if method == "Target.attachToTarget" {
			return `{"sessionId":"S1"}`, nil
		}

		return `{}`, nil
	}}

	e, _ := filmedEngine(t, chrome.serve(t), "d-1")

	f, err := e.Film(t.Context(), "d-1", Size{}, func(Frame) {})
	require.NoError(t, err)
	assert.Equal(t, standardPage, f.Size(), "the tab's own size")

	chrome.push <- `{"method":"Target.detachedFromTarget","params":{"sessionId":"S1","targetId":"T1"}}`

	select {
	case <-f.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the film did not end with its tab")
	}

	f.Stop(t.Context())

	_, err = e.Film(t.Context(), "d-2", Size{}, func(Frame) {})
	require.ErrorIs(t, err, ErrNoPage)
}

// An element's box is agent-browser's `get box` of it; one it cannot find is the agent's mistake, with its words.
func TestAnElementsBoxIsAgentBrowsers(t *testing.T) {
	t.Parallel()

	e, log := filmedEngine(t, (&fakeChrome{answer: func(string, string) (string, []string) { return `{}`, nil }}).serve(t),
		"d-1")

	_, err := e.Box(t.Context(), "d-1", "#nothing")
	require.ErrorIs(t, err, ErrBadArguments)
	assert.Contains(t, err.Error(), "Element not found: #nothing")

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(log), "box"),
		[]byte(`{"x":10.5,"y":20,"width":100,"height":40}`), 0o600))

	box, err := e.Box(t.Context(), "d-1", "@e3")
	require.NoError(t, err)
	assert.Equal(t, Box{X: 10.5, Y: 20, Width: 100, Height: 40}, box)
	assert.Contains(t, logged(t, log), `d-1 mcp call agent_browser_get_box - --tools all {"selector":"@e3"}`)
}
