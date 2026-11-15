package clientapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	shared "github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// sharedBrowser stands for the browser every agent shares: it has the tabs
// it is given, and a view of any of them gives the page `updates` and keeps
// what the page did.
type sharedBrowser struct {
	tabs    []shared.Tab
	held    bool
	watched chan string
	updates chan shared.Update
	did     chan shared.Action
	closed  chan bool
}

func newSharedBrowser(tabs ...shared.Tab) *sharedBrowser {
	return &sharedBrowser{
		tabs: tabs, watched: make(chan string, 4), updates: make(chan shared.Update, 4),
		did: make(chan shared.Action, 4), closed: make(chan bool, 4),
	}
}

func (b *sharedBrowser) Status() shared.Status { return shared.Status{Installed: true, Running: true} }

func (b *sharedBrowser) Tabs(context.Context) ([]shared.Tab, bool) { return b.tabs, b.held }

func (b *sharedBrowser) Watch(_ context.Context, tab string) (BrowserView, error) {
	b.watched <- tab

	if tab == "d-gone" {
		return nil, shared.ErrNoSuchTab
	}

	return b, nil
}

func (b *sharedBrowser) Next(ctx context.Context) (shared.Update, error) {
	select {
	case up := <-b.updates:
		return up, nil
	case <-ctx.Done():
		return shared.Update{}, ctx.Err()
	}
}

func (b *sharedBrowser) Do(_ context.Context, a shared.Action) error {
	b.did <- a

	return nil
}

func (b *sharedBrowser) Close(_ context.Context, clean bool) { b.closed <- clean }

// ticket asks for a view of a tab and returns the WebSocket's address.
func (w *world) viewTicket(c *openapi.ClientWithResponses, tab string) string {
	w.t.Helper()

	resp, err := c.OpenBrowserViewWithResponse(w.t.Context(), tab)
	require.NoError(w.t, err)
	require.NotNil(w.t, resp.JSON201, "a ticket: %s", resp.Body)
	assert.WithinDuration(w.t, time.Now().Add(viewTicketFor), resp.JSON201.ExpiresAt, 5*time.Second)

	return "ws" + strings.TrimPrefix(w.url, "http") + BasePath + "/browser/views/" + resp.JSON201.Ticket
}

// Scenario: the owner looks at the shared browser. Each tab says whose it
// is, a worker's with its Dispatch. Nobody else is told, and a control plane
// with no browser says so.
func TestTheOwnerSeesTheBrowserAndItsTabs(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	none, err := owner.GetBrowserWithResponse(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, none.StatusCode())

	b := newSharedBrowser(
		shared.Tab{ID: shared.OwnerTab, URL: "https://accounts.example.com/", Title: "Sign in", Views: 1},
		shared.Tab{ID: "d-dsp_1", URL: "https://example.com/", Title: "Example Domain"},
		shared.Tab{ID: "c-tok_1"}, shared.Tab{ID: "o-tok_2"}, shared.Tab{ID: "b-cht_1"},
	)
	b.held = true
	w.srv.WithBrowser(b)

	resp, err := owner.GetBrowserWithResponse(t.Context())
	require.NoError(t, err)
	require.NotNil(t, resp.JSON200, "%s", resp.Body)
	assert.True(t, resp.JSON200.Installed)
	assert.True(t, resp.JSON200.Running)
	assert.True(t, resp.JSON200.Held)
	require.Len(t, resp.JSON200.Tabs, 5)

	kinds := make([]openapi.BrowserTabKind, 0, 5)
	for _, tab := range resp.JSON200.Tabs {
		kinds = append(kinds, tab.Kind)
	}

	assert.Equal(t, []openapi.BrowserTabKind{
		openapi.BrowserTabKindOwner, openapi.BrowserTabKindWorker, openapi.BrowserTabKindCoordinator,
		openapi.BrowserTabKindToken, openapi.BrowserTabKindCoordinator,
	}, kinds)

	mine, workers := resp.JSON200.Tabs[0], resp.JSON200.Tabs[1]
	assert.Equal(t, "Sign in", mine.Title)
	assert.Equal(t, 1, mine.Views)
	assert.Equal(t, "https://example.com/", workers.Url)
	require.NotNil(t, workers.DispatchId)
	assert.Equal(t, "dsp_1", *workers.DispatchId)

	refused, err := pi.GetBrowserWithResponse(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "the browser is the owner's to look at")

	ticket, err := pi.OpenBrowserViewWithResponse(t.Context(), shared.OwnerTab)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, ticket.StatusCode())
}

// Scenario: every tab names the chat it belongs to, whichever agent opened
// it (issue #557). A worker's is its Run's chat's and a Bot's pi's is its
// own chat's, with its Bot; a worker of a Run the owner made, a coordinator
// token's tab and a pi's of a chat that is gone belong to no chat.
func TestATabNamesItsChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	ctx := t.Context()

	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, vmap.ID, "maps")
	require.NoError(t, err)

	ofChat, err := w.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "add a health page", CreatedBy: "pi", Repo: "kl3inIT/demo", ChatID: chat.ID,
	})
	require.NoError(t, err)
	ofOwner := w.createRun(owner, "tidy the docs")

	node, _, err := w.svc.AddNode(ctx, "box", 0)
	require.NoError(t, err)
	require.NoError(t, w.svc.NodeHello(ctx, node.ID, orchestration.Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude"}},
	}))
	// The starting profiles run under the Claude subscription, served under an account the control plane keeps.
	_, err = w.svc.AddTokenAccount(ctx, "claude", "personal", func(id string) string { return "vault:" + id })
	require.NoError(t, err)

	for _, run := range []string{ofChat.ID, ofOwner.Id} {
		made, err := owner.CreateTasksWithResponse(ctx, run, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
			{Ref: "a", Spec: "work"},
		}})
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	}

	scheduled, err := w.svc.Schedule(ctx)
	require.NoError(t, err)
	require.Len(t, scheduled.Created, 2)

	tabs := []shared.Tab{{ID: "b-" + chat.ID}, {ID: "b-cht_gone"}, {ID: "c-tok_1"}}
	for _, dispatch := range scheduled.Created {
		tabs = append(tabs, shared.Tab{ID: "d-" + dispatch})
	}

	w.srv.WithBrowser(newSharedBrowser(tabs...))

	resp, err := owner.GetBrowserWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, resp.JSON200, "%s", resp.Body)

	chatOf := map[string]string{}
	runOf := map[string]string{}

	for _, tab := range resp.JSON200.Tabs {
		if tab.ChatId != nil {
			chatOf[tab.Id] = *tab.ChatId
		}

		if tab.RunId != nil {
			runOf[tab.Id] = *tab.RunId
		}
	}

	bots := resp.JSON200.Tabs[0]
	assert.Equal(t, openapi.BrowserTabKindCoordinator, bots.Kind)
	require.NotNil(t, bots.BotId)
	assert.Equal(t, vmap.ID, *bots.BotId, "a Bot's tab says whose Bot it is")

	want := map[string]string{"b-" + chat.ID: chat.ID}

	for _, dispatch := range scheduled.Created {
		if runOf["d-"+dispatch] == ofChat.ID {
			want["d-"+dispatch] = chat.ID
		}
	}

	assert.Len(t, want, 2, "one worker is of the chat's Run")
	assert.Equal(t, want, chatOf)
}

// Scenario: the owner watches a tab. The ticket opens the WebSocket once.
// The page is told who controls, the tab's size and address as JSON and
// gets a frame as a binary message; what it sends reaches the view; and
// when the owner closes the page the view is closed as the owner's doing.
func TestTheOwnerWatchesATabThroughATicket(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)

	b := newSharedBrowser()
	w.srv.WithBrowser(b)

	target := w.viewTicket(owner, shared.OwnerTab)

	conn, _, err := websocket.Dial(t.Context(), target, nil) //nolint:bodyclose // A WebSocket: no body is left open.
	require.NoError(t, err)
	assert.Equal(t, shared.OwnerTab, <-b.watched)

	b.updates <- shared.Update{
		Controller: shared.ControllerAgent, Viewport: &shared.Size{Width: 1280, Height: 720},
		URL: "https://example.com/", JPEG: []byte("a frame"),
	}

	for _, want := range []string{
		`{"controller":"agent","type":"control"}`, `{"height":720,"type":"viewport","width":1280}`,
		`{"type":"url","url":"https://example.com/"}`,
	} {
		kind, data, err := conn.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, websocket.MessageText, kind)
		assert.JSONEq(t, want, string(data))
	}

	kind, frame, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, kind)
	assert.Equal(t, "a frame", string(frame))

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`not JSON`)))
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText,
		[]byte(`{"type":"mouse","action":"down","x":10.5,"y":20,"button":"left","clickCount":1}`)))
	assert.Equal(t, shared.Action{Type: "mouse", Action: "down", X: 10.5, Y: 20, Button: "left", ClickCount: 1}, <-b.did)

	_, _, err = websocket.Dial(t.Context(), target, nil) //nolint:bodyclose // The dial fails: no body is left open.
	require.Error(t, err, "a ticket opens its WebSocket once")

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, ""))
	assert.True(t, <-b.closed, "the owner closed the page: the view is closed as the owner's doing")
}

// A page that goes away without closing its view leaves the view dropped,
// not closed by the owner; and a tab that is gone is said to be.
func TestAViewThatDropsIsNotClosedByTheOwner(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)

	b := newSharedBrowser()
	w.srv.WithBrowser(b)

	//nolint:bodyclose // A WebSocket: no body is left open.
	conn, _, err := websocket.Dial(t.Context(), w.viewTicket(owner, "d-dsp_1"), nil)
	require.NoError(t, err)
	assert.Equal(t, "d-dsp_1", <-b.watched)

	require.NoError(t, conn.CloseNow())
	assert.False(t, <-b.closed)

	//nolint:bodyclose // A WebSocket: no body is left open.
	gone, _, err := websocket.Dial(t.Context(), w.viewTicket(owner, "d-gone"), nil)
	require.NoError(t, err)

	_, _, err = gone.Read(t.Context())
	assert.Equal(t, websocket.StatusCode(4404), websocket.CloseStatus(err))
}
