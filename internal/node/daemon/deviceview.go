package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The owner's view of the phone (issue #417, step 3) joins as a terminal
// does (terminal.go): the control plane asks on the socket the node holds,
// and the node opens a second connection for the view's frames and input,
// naming the stream it was given. The view's frames are the phone's; the
// node passes the owner's input to the phone as it comes: the control plane
// sends only what the owner may send.

// PhoneView is one view of the phone's screen; *device.View is the node's.
type PhoneView interface {
	// Next returns the next frame, or a text message, of the view.
	Next(ctx context.Context) ([]byte, *nodeproto.DeviceViewMessage, error)
	// Input carries out one of the owner's input messages on the phone.
	Input(in nodeproto.DeviceViewMessage) error
	Close()
}

// openDeviceView answers a device_view: it dials the view's connection and
// serves the view on it until either ends, on the daemon's own context, as a
// terminal is served.
func (d *Daemon) openDeviceView(payload json.RawMessage) error {
	var open nodeproto.DeviceView
	if err := json.Unmarshal(payload, &open); err != nil {
		return fmt.Errorf("decoding a phone view request: %w", err)
	}

	if d.cfg.WatchPhone == nil || d.base == nil {
		return nil
	}

	go func() {
		ctx := d.base

		if err := d.serveDeviceView(ctx, open); err != nil && ctx.Err() == nil {
			d.logger.WarnContext(ctx, "the phone's view ended",
				slog.String("event", "node.device_view_failed"), slog.String("error", err.Error()))
		}
	}()

	return nil
}

func (d *Daemon) serveDeviceView(ctx context.Context, open nodeproto.DeviceView) error {
	target := "ws" + strings.TrimPrefix(strings.TrimSuffix(d.cfg.URL, "/"), "http") + nodeproto.PathDevice +
		"?stream=" + url.QueryEscape(open.StreamID)

	dialCtx, cancel := context.WithTimeout(ctx, nodeproto.TerminalMeetWithin)
	defer cancel()

	conn, resp, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{
		HTTPClient: d.http, HTTPHeader: http.Header{"Authorization": {"Bearer " + d.cfg.Token}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		return fmt.Errorf("opening the phone view's connection: %w", err)
	}

	conn.SetReadLimit(nodeproto.MaxMessageBytes)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	view := d.cfg.WatchPhone(ctx)
	defer view.Close()

	d.logger.InfoContext(ctx, "the phone's view opened", slog.String("event", "node.device_view_opened"))

	// The owner's input, until the control plane closes the view.
	go func() {
		defer stop()

		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}

			var in nodeproto.DeviceViewMessage
			if json.Unmarshal(raw, &in) != nil {
				continue
			}

			if err := view.Input(in); err != nil {
				d.logger.WarnContext(ctx, "the owner's input did not reach the phone",
					slog.String("event", "node.device_input_failed"), slog.String("error", err.Error()))
			}
		}
	}()

	for {
		frame, said, err := view.Next(ctx)
		if err != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "the view ended")

			if ctx.Err() != nil {
				return nil
			}

			return err
		}

		if frame != nil {
			err = conn.Write(ctx, websocket.MessageBinary, frame)
		} else {
			var raw []byte

			raw, err = json.Marshal(said)
			if err == nil {
				err = conn.Write(ctx, websocket.MessageText, raw)
			}
		}

		if err != nil {
			_ = conn.Close(websocket.StatusGoingAway, "")

			return nil
		}
	}
}
