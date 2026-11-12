package clientcli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// twoTabs stands for the shared browser, running with the owner's tab and a
// worker's, the owner holding it.
type twoTabs struct{}

func (twoTabs) Status() browser.Status { return browser.Status{Installed: true, Running: true} }

func (twoTabs) Tabs(context.Context) ([]browser.Tab, bool) {
	return []browser.Tab{
		{ID: browser.OwnerTab, URL: "https://accounts.example.com/", Title: "Sign in", Views: 1},
		{ID: "d-dsp_1", URL: "https://example.com/", Title: "Example Domain"},
	}, true
}

func (twoTabs) Watch(context.Context, string) (clientapi.BrowserView, error) {
	return nil, browser.ErrNoSuchTab
}

// The owner asks what the shared browser is doing: one line for the browser,
// one for each tab.
func TestBrowserStatusListsTheTabs(t *testing.T) {
	t.Parallel()

	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithBrowser(twoTabs{}) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "browser", "status")
	require.NoError(t, err)
	assert.Equal(t, "running, the owner holds it, 2 tabs\n"+
		"owner\towner\thttps://accounts.example.com/\tSign in\n"+
		"d-dsp_1\tworker\thttps://example.com/\tExample Domain\n", out)

	var status openapi.Browser

	w.jsonCmd(t, &status, "", "browser", "status")
	assert.True(t, status.Held)
	require.Len(t, status.Tabs, 2)
	assert.Equal(t, openapi.BrowserTabKindWorker, status.Tabs[1].Kind)
}
