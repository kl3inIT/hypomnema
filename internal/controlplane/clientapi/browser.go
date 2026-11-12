package clientapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	shared "github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The owner watching the browser every agent shares (issue #219).
//
// As for a terminal (terminal.go), the page first asks for a ticket with an
// authorized request and then opens a WebSocket the ticket names, which works
// once and for a short time. On it the page receives a tab's frames as binary
// JPEG messages, each of which it acknowledges before the next one comes, and
// JSON messages for the tab's size, its address and who controls; it sends
// JSON messages for taking and releasing control and, while it holds control,
// for the mouse, the keys and navigation. The messages are T3 Code's
// (apps/server/src/preview/ServerBrowser.ts).

const (
	// viewTicketFor is how long a ticket waits for its WebSocket.
	viewTicketFor = 30 * time.Second
	// viewMessageBytes bounds one message from the page.
	viewMessageBytes = 64 << 10
	// viewWriteWithin is how long the page has to take a message. A page that takes longer is disconnected:
	// T3 Code cuts a viewer off with 8 MiB waiting, which this WebSocket does not show.
	viewWriteWithin = 10 * time.Second
)

// BrowserView is the owner's view of one tab.
type BrowserView interface {
	// Next waits for what the page is to be given next.
	Next(ctx context.Context) (shared.Update, error)
	// Do carries out one message of the page.
	Do(ctx context.Context, a shared.Action) error
	// Close ends the view; clean says the owner closed it.
	Close(ctx context.Context, clean bool)
}

// Browser is the browser every agent shares.
type Browser interface {
	Status() shared.Status
	Tabs(ctx context.Context) (tabs []shared.Tab, held bool)
	Watch(ctx context.Context, tab string) (BrowserView, error)
}

// WithBrowser sets the shared browser. Without it its routes answer that
// there is none.
func (s *Server) WithBrowser(b Browser) *Server {
	s.browser = b

	return s
}

// viewTicket is one permission to open a view's WebSocket.
type viewTicket struct {
	tab string
	// by and from say who asked, for the record.
	by      string
	from    string
	expires time.Time
}

// viewTickets are the tickets not used yet.
type viewTickets struct {
	mu      sync.Mutex
	tickets map[string]viewTicket
}

func (t *viewTickets) add(ticket viewTicket) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a browser view's ticket: %w", err)
	}

	id := hex.EncodeToString(raw)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.tickets == nil {
		t.tickets = map[string]viewTicket{}
	}

	now := time.Now()
	for old, tk := range t.tickets {
		if now.After(tk.expires) {
			delete(t.tickets, old)
		}
	}

	t.tickets[id] = ticket

	return id, nil
}

// take returns a ticket once, if it has not expired.
func (t *viewTickets) take(id string) (viewTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ticket, ok := t.tickets[id]
	delete(t.tickets, id)

	return ticket, ok && time.Now().Before(ticket.expires)
}

var errNoBrowser = fmt.Errorf("%w: this control plane has no browser", orchestration.ErrUnavailable)

// GetBrowser says what the shared browser is doing and lists its tabs.
func (s *Server) GetBrowser(ctx context.Context, _ openapi.GetBrowserRequestObject,
) (openapi.GetBrowserResponseObject, error) {
	if s.browser == nil {
		return nil, errNoBrowser
	}

	status := s.browser.Status()
	tabs, held := s.browser.Tabs(ctx)

	out := openapi.Browser{
		Installed: status.Installed, Running: status.Running, Held: held, Tabs: make([]openapi.BrowserTab, 0, len(tabs)),
	}

	for _, t := range tabs {
		out.Tabs = append(out.Tabs, s.browserTab(ctx, t))
	}

	return openapi.GetBrowser200JSONResponse(out), nil
}

// browserTab says whose a tab is. An agent's name in the browser begins with
// the kind of its credential (agentapi).
func (s *Server) browserTab(ctx context.Context, t shared.Tab) openapi.BrowserTab {
	tab := openapi.BrowserTab{Id: t.ID, Kind: openapi.BrowserTabKindToken, Url: t.URL, Title: t.Title, Views: t.Views}

	switch {
	case t.ID == shared.OwnerTab:
		tab.Kind = openapi.BrowserTabKindOwner
	case strings.HasPrefix(t.ID, "c-"):
		tab.Kind = openapi.BrowserTabKindCoordinator
	case strings.HasPrefix(t.ID, "d-"):
		tab.Kind = openapi.BrowserTabKindWorker

		id := strings.TrimPrefix(t.ID, "d-")
		tab.DispatchId = &id

		if d, err := s.svc.GetDispatch(ctx, id); err == nil {
			tab.TaskId = &d.TaskID

			if task, err := s.svc.GetTask(ctx, d.TaskID); err == nil {
				tab.RunId = &task.RunID
			}
		}
	}

	return tab
}

// OpenBrowserView gives the owner a ticket to watch a tab.
func (s *Server) OpenBrowserView(ctx context.Context, req openapi.OpenBrowserViewRequestObject,
) (openapi.OpenBrowserViewResponseObject, error) {
	if s.browser == nil {
		return nil, errNoBrowser
	}

	expires := time.Now().Add(viewTicketFor)

	ticket, err := s.views.add(viewTicket{
		tab: req.TabId, by: principal(ctx).Name, from: callerOf(ctx).address, expires: expires,
	})
	if err != nil {
		return nil, err
	}

	return openapi.OpenBrowserView201JSONResponse{Ticket: ticket, ExpiresAt: expires.UTC()}, nil
}

// browserViewSocket is a view's WebSocket. The ticket in its path is the
// permission; the origin must be the web client's own, which the WebSocket
// library checks against the request's host.
func (s *Server) browserViewSocket(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.views.take(r.PathValue("ticket"))
	if !ok || s.browser == nil {
		http.Error(w, "unknown or expired browser view ticket", http.StatusNotFound)

		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(viewMessageBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// A shutdown ends the view with the server's other long requests.
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	view, err := s.browser.Watch(ctx, ticket.tab)
	if err != nil {
		status := websocket.StatusTryAgainLater
		if errors.Is(err, shared.ErrNoSuchTab) {
			// T3 Code's close code for a tab that is gone.
			status = websocket.StatusCode(4404)
		}

		_ = conn.Close(status, closeReason(err.Error()))

		return
	}

	who := []any{slog.String("tab", ticket.tab), slog.String("by", ticket.by), slog.String("from", ticket.from)}
	started := time.Now()

	s.logger.InfoContext(ctx, "browser view opened",
		append([]any{slog.String("event", "clientapi.browser_view_opened")}, who...)...)

	clean, reason := copyBrowserView(ctx, conn, view)

	closing := context.WithoutCancel(ctx)

	view.Close(closing, clean)

	s.logger.InfoContext(closing, "browser view closed", append([]any{
		slog.String("event", "clientapi.browser_view_closed"),
		slog.Duration("lasted", time.Since(started).Round(time.Second)), slog.String("reason", reason),
	}, who...)...)
}

// copyBrowserView copies between the page and the view until either leaves.
// It reports whether the page closed the view itself, and why the view ended
// when it was not the page that left.
func copyBrowserView(ctx context.Context, conn *websocket.Conn, view BrowserView) (clean bool, reason string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	viewLeft := make(chan string, 1)

	go func() {
		reason := ""

		defer func() {
			viewLeft <- reason

			_ = conn.Close(websocket.StatusNormalClosure, closeReason(reason))
		}()

		for {
			up, err := view.Next(ctx)
			if err != nil {
				if ctx.Err() == nil {
					reason = err.Error()
				}

				return
			}

			if err := writeBrowserUpdate(ctx, conn, up); err != nil {
				if ctx.Err() == nil {
					reason = "the page did not keep up"
				}

				return
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// The page closing its socket, or leaving for another page, is the owner closing the view.
			status := websocket.CloseStatus(err)
			clean = status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway

			break
		}

		var action shared.Action
		if json.Unmarshal(data, &action) != nil {
			continue
		}

		if err := view.Do(ctx, action); err != nil {
			break
		}
	}

	cancel()

	reason = <-viewLeft

	return clean && reason == "", reason
}

// writeBrowserUpdate sends the page what a view has for it: a frame as a
// binary message, the rest as JSON.
func writeBrowserUpdate(ctx context.Context, conn *websocket.Conn, up shared.Update) error {
	ctx, cancel := context.WithTimeout(ctx, viewWriteWithin)
	defer cancel()

	var messages []map[string]any

	if up.Controller != "" {
		messages = append(messages, map[string]any{"type": "control", "controller": up.Controller})
	}

	if up.Viewport != nil {
		messages = append(messages,
			map[string]any{"type": "viewport", "width": up.Viewport.Width, "height": up.Viewport.Height})
	}

	if up.URL != "" {
		messages = append(messages, map[string]any{"type": "url", "url": up.URL})
	}

	for _, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}

		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			return err
		}
	}

	if up.JPEG != nil {
		return conn.Write(ctx, websocket.MessageBinary, up.JPEG)
	}

	return nil
}
