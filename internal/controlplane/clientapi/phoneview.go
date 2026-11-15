package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
)

// The owner watching the phone and taking it over (issue #417, step 3), as
// the owner watches the browser (browser.go): a ticket from an authorized
// request, then a WebSocket the ticket names, once and for a short time. On
// it the page receives the screen as binary messages, H.264 behind serve-emu's
// SEMU header, and JSON messages for who controls, the phone's state and the
// screen's size; it sends JSON for taking and releasing control, a key frame
// asked for, and, while it holds control, the touches, keys and texts of
// serve-emu's input (T3 Code's Android client).

// PhoneView is the owner's view of the phone.
type PhoneView interface {
	Next(ctx context.Context) (phone.Update, error)
	Do(ctx context.Context, a phone.Action) error
	Close(ctx context.Context, clean bool)
}

// Phone is the phone the owner watches.
type Phone interface {
	Watch(ctx context.Context) (PhoneView, error)
	// Controlled says whether the owner holds the phone.
	Controlled() bool
}

// WithPhone sets the phone the owner watches. Without it its view routes
// answer that there is none.
func (s *Server) WithPhone(p Phone) *Server {
	s.phone = p

	return s
}

var errNoPhone = fmt.Errorf("%w: this control plane shows no phone", orchestration.ErrUnavailable)

// OpenDeviceView gives the owner a ticket to watch the phone.
func (s *Server) OpenDeviceView(ctx context.Context, _ openapi.OpenDeviceViewRequestObject,
) (openapi.OpenDeviceViewResponseObject, error) {
	if s.phone == nil {
		return nil, errNoPhone
	}

	expires := time.Now().Add(viewTicketFor)

	ticket, err := s.phoneViews.add(viewTicket{by: principal(ctx).Name, from: callerOf(ctx).address, expires: expires})
	if err != nil {
		return nil, err
	}

	return openapi.OpenDeviceView201JSONResponse{Ticket: ticket, ExpiresAt: expires.UTC()}, nil
}

// phoneViewSocket is a phone view's WebSocket. The ticket in its path is the
// permission; the origin must be the web client's own.
func (s *Server) phoneViewSocket(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.phoneViews.take(r.PathValue("ticket"))
	if !ok || s.phone == nil {
		http.Error(w, "unknown or expired phone view ticket", http.StatusNotFound)

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

	view, err := s.phone.Watch(ctx)
	if err != nil {
		status := websocket.StatusTryAgainLater
		if errors.Is(err, phone.ErrUnavailable) {
			// The browser's close code for a tab that is not there: nothing to try again until the owner asks.
			status = phoneGone
		}

		_ = conn.Close(status, closeReason(err.Error()))

		return
	}

	who := []any{slog.String("by", ticket.by), slog.String("from", ticket.from)}
	started := time.Now()

	s.logger.InfoContext(ctx, "phone view opened",
		append([]any{slog.String("event", "clientapi.phone_view_opened")}, who...)...)

	clean, reason := copyPhoneView(ctx, conn, view)

	closing := context.WithoutCancel(ctx)

	view.Close(closing, clean)

	s.logger.InfoContext(closing, "phone view closed", append([]any{
		slog.String("event", "clientapi.phone_view_closed"),
		slog.Duration("lasted", time.Since(started).Round(time.Second)), slog.String("reason", reason),
	}, who...)...)
}

// phoneGone is the close code of a view of a phone that cannot be shown now,
// the browser's code for a tab that is gone.
const phoneGone = websocket.StatusCode(4404)

// copyPhoneView copies between the page and the view until either leaves, as
// copyBrowserView does.
func copyPhoneView(ctx context.Context, conn *websocket.Conn, view PhoneView) (clean bool, reason string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	viewLeft := make(chan string, 1)

	go func() {
		reason, status := "", websocket.StatusNormalClosure

		defer func() {
			viewLeft <- reason

			_ = conn.Close(status, closeReason(reason))
		}()

		for {
			up, err := view.Next(ctx)
			if err != nil {
				if ctx.Err() == nil {
					reason = err.Error()
				}

				// A phone that cannot be shown is not tried again until the owner asks (the browser's tab gone).
				if errors.Is(err, phone.ErrUnavailable) {
					status = phoneGone
				}

				return
			}

			if err := writePhoneUpdate(ctx, conn, up); err != nil {
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

		var action phone.Action
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

// writePhoneUpdate sends the page what a view has for it: a frame as a
// binary message, the rest as JSON.
func writePhoneUpdate(ctx context.Context, conn *websocket.Conn, up phone.Update) error {
	ctx, cancel := context.WithTimeout(ctx, viewWriteWithin)
	defer cancel()

	switch {
	case up.Frame != nil:
		return conn.Write(ctx, websocket.MessageBinary, up.Frame)
	case up.Controller != "":
		return writeJSON(ctx, conn, map[string]any{"type": "control", "controller": up.Controller})
	case up.Said != nil:
		return writeJSON(ctx, conn, up.Said)
	}

	return nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, message any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}

	return conn.Write(ctx, websocket.MessageText, raw)
}
