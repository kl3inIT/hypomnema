package agentapi_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// filmingBrowser stands for the browser's takes: it records what each agent asked.
type filmingBrowser struct {
	mu    sync.Mutex
	asked []string
	size  browser.Size
}

func (f *filmingBrowser) Start(_ context.Context, c browsertake.Caller, title, _ string, viewport browser.Size,
) (devicetool.Take, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked, f.size = append(f.asked, c.Key+" start "+title+" "+c.Work), viewport

	return devicetool.Take{ID: "tk_00000000000000aa"}, "Take tk_00000000000000aa is recording.", false
}

func (f *filmingBrowser) Say(_ context.Context, c browsertake.Caller, take, text, chapter, element string,
) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, c.Key+" say "+take+" "+text+" "+chapter+" "+element)

	return "Said at 0:01 of take " + take + ".", false
}

func (f *filmingBrowser) Stop(_ context.Context, c browsertake.Caller, take string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, c.Key+" stop "+take)

	return "Take " + take + " is finalizing.", false
}

// Scenario: a worker with the browser is given the browser take's tools beside the browser's, and films its own
// tab: each call reaches the takes as the worker, with its Dispatch as the work that ends the take, the viewport it
// gave and the element of each step. Arguments out of bounds are refused before they reach the takes. A profile
// that turns the browser off by its family turns them off too.
func TestAWorkerFilmsItsTabWithTheBrowsersTakeTools(t *testing.T) {
	t.Parallel()

	takes := &filmingBrowser{}
	w := newWorldWith(t, 0, func(s *agentapi.Server) { s.WithBrowserTakes(takes) })
	key := w.workerOf("laptop", "")

	names := toolNames(w.tools(key))
	for _, tool := range []string{devicetool.BrowserVideoStart, devicetool.BrowserVideoSay, devicetool.BrowserVideoStop} {
		assert.Contains(t, names, tool)
	}

	status, text, failed := w.use(key, devicetool.BrowserVideoStart,
		`{"title":"Đăng nhập","width":390,"height":844}`)
	require.Equal(t, http.StatusOK, status, text)
	assert.False(t, failed, text)

	_, text, failed = w.use(key, devicetool.BrowserVideoSay,
		`{"take":"tk_00000000000000aa","text":"Chạm vào Đăng nhập","element":"@e3","chapter":"Mở"}`)
	assert.False(t, failed, text)

	_, text, failed = w.use(key, devicetool.BrowserVideoStop, `{"take":"tk_00000000000000aa"}`)
	assert.False(t, failed, text)

	_, text, failed = w.use(key, devicetool.BrowserVideoStart, `{"title":"Đăng nhập","width":390}`)
	assert.True(t, failed)
	assert.Contains(t, text, "width and height both")

	_, text, failed = w.use(key, devicetool.BrowserVideoSay, `{"take":"tk_00000000000000aa","text":" "}`)
	assert.True(t, failed)
	assert.Contains(t, text, "text is the line said")

	takes.mu.Lock()
	asked, size := takes.asked, takes.size
	takes.mu.Unlock()

	require.Len(t, asked, 3, "a refused call reaches no take")
	assert.Regexp(t, `^\S+ start Đăng nhập dispatch \S+$`, asked[0])
	assert.Contains(t, asked[1], " say tk_00000000000000aa Chạm vào Đăng nhập Mở @e3")
	assert.Contains(t, asked[2], " stop tk_00000000000000aa")
	assert.Equal(t, browser.Size{Width: 390, Height: 844}, size)

	off := w.workerUnder("tools_off: [agent_browser_*]\n", "laptop-2", "")
	assert.NotContains(t, toolNames(w.tools(off)), devicetool.BrowserVideoStart)

	status, _, _ = w.use(off, devicetool.BrowserVideoStart, `{"title":"x"}`)
	assert.Equal(t, http.StatusNotFound, status)
}
