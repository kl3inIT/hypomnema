package phone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/controlplane/ownerhold"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The owner watching the phone and taking it over (issue #417, step 3), in
// the shape of the browser's view (internal/controlplane/browser, view.go):
// the node that holds the phone streams its screen (internal/node/device,
// view.go) on a connection of its own, and a View passes it to the page and
// puts the control gate in front of the owner's input, with the browser's
// rules (package ownerhold): while the owner holds the phone every agent's
// call is answered that the tools are paused. A view of a phone whose machine
// is stopped starts the machine, as an agent's call does, and the node starts
// the phone.

const (
	// maxViews is how many views may be open, as the browser's.
	maxViews = 4
	// StateWaking is the phone's state while its machine starts: the control plane's word, not the node's.
	StateWaking = "waking"
	// The controller a view is told, as the browser's.
	ControllerAgent = "agent"
	ControllerYou   = "you"
	// maxKeycode bounds an Android key code (KEYCODE_* end below 400).
	maxKeycode = 400
)

// ErrTooManyViews reports that as many views are open as may be.
var ErrTooManyViews = errors.New("too many phone views are open; close one first")

// ViewEnd is the node's side of a view (nodeapi.DeviceViewEnd).
type ViewEnd interface {
	Read(ctx context.Context) ([]byte, *nodeproto.DeviceViewMessage, error)
	Send(ctx context.Context, in nodeproto.DeviceViewMessage) error
	Close(reason string)
}

// Update is what a view has for the page; one part is set.
type Update struct {
	// Frame is a frame of the screen: H.264 behind serve-emu's SEMU header.
	Frame []byte
	// Said is the phone's state, its screen's size, or why the stream could not start, as the node said it.
	Said *nodeproto.DeviceViewMessage
	// Controller is who drives the phone, ControllerAgent or ControllerYou.
	Controller string
}

// View is the owner's view of the phone.
type View struct {
	p    *Phone
	stop context.CancelFunc
	said chan Update
	// again asks Next to say who controls once more: an input was dropped.
	again chan struct{}

	mu  sync.Mutex
	end ViewEnd
	// reason is why the view ended; ended wraps ErrUnavailable when the phone could not be reached at all, and
	// is plain when the node's view dropped, which the page tries again.
	reason error
	told   bool
	held   bool

	closing sync.Once
}

// Watch opens the owner's view of the phone. The view connects on its own,
// waking the phone's machine first when it is stopped, and says how it goes.
func (p *Phone) Watch(ctx context.Context) (*View, error) {
	if p.cfg.Views == nil {
		return nil, fmt.Errorf("%w: this control plane does not show the phone", ErrUnavailable)
	}

	n, err := p.node(ctx)
	if err != nil {
		return nil, err
	}

	if !p.hold.Opened(maxViews) {
		return nil, ErrTooManyViews
	}

	p.busy()

	// The view outlives the request that opened it.
	streaming, stop := context.WithCancel(context.WithoutCancel(ctx))
	v := &View{p: p, stop: stop, said: make(chan Update, 8), again: make(chan struct{}, 1)}

	go v.run(streaming, n.ID)

	return v, nil
}

// run connects the view to the node and passes on what the node says, until
// either ends.
func (v *View) run(ctx context.Context, nodeID string) {
	defer close(v.said)

	end, err := v.connect(ctx, nodeID)
	if err != nil {
		v.ended(err)

		return
	}

	// Closed here too: a view closed before the end was kept here would leave it open.
	defer end.Close("the owner's view closed")

	v.mu.Lock()
	v.end = end
	v.mu.Unlock()

	for {
		frame, said, err := end.Read(ctx)
		if err != nil {
			v.ended(errors.New("the phone's view ended"))

			return
		}

		up := Update{Frame: frame, Said: said}

		select {
		case v.said <- up:
		case <-ctx.Done():
			return
		}
	}
}

// connect has the phone's node online, waking its machine and saying so while
// it starts, and opens the view on it.
func (v *View) connect(ctx context.Context, nodeID string) (ViewEnd, error) {
	p := v.p

	for !p.cfg.Link.Online(nodeID) {
		n, err := p.node(ctx)
		if err != nil {
			return nil, err
		}

		select {
		case v.said <- Update{Said: &nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewState, State: StateWaking}}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		// A view waits for as long as the owner watches; a call gives up after WakeWithin.
		if err := p.wake(ctx, n); err != nil && !errors.Is(err, errWaking) {
			return nil, err
		}
	}

	end, err := p.cfg.Views(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return end, nil
}

func (v *View) ended(reason error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.reason == nil {
		v.reason = reason
	}
}

// Next waits for what the page is to be given next. It fails when the view
// ended, with why.
func (v *View) Next(ctx context.Context) (Update, error) {
	for {
		held, changed := v.p.hold.State()

		v.mu.Lock()
		say := !v.told || held != v.held
		v.told, v.held = true, held
		v.mu.Unlock()

		if say {
			if held {
				return Update{Controller: ControllerYou}, nil
			}

			return Update{Controller: ControllerAgent}, nil
		}

		select {
		case up, ok := <-v.said:
			if !ok {
				v.mu.Lock()
				reason := v.reason
				v.mu.Unlock()

				if reason == nil {
					reason = context.Canceled
				}

				return Update{}, reason
			}

			return up, nil
		case <-changed:
		case <-v.again:
			v.mu.Lock()
			v.told = false
			v.mu.Unlock()
		case <-ctx.Done():
			return Update{}, ctx.Err()
		}
	}
}

// Action is one message of the page: takeControl and releaseControl, as the
// browser's; reset-video, which needs no control; and the input of
// nodeproto.DeviceViewMessage, which needs it.
type Action = nodeproto.DeviceViewMessage

// Do carries out one message of the page. Input needs control: without it it
// is dropped and the page is told again who controls.
func (v *View) Do(ctx context.Context, a Action) error {
	now := v.p.cfg.Clock.Now()

	switch a.Type {
	case "takeControl":
		v.p.hold.Take(ctx, now)
		v.p.cfg.Logger.InfoContext(ctx, "the owner took the phone", slog.String("event", "phone.taken"))

		return nil
	case "releaseControl":
		if v.p.hold.Release() {
			v.p.released(ctx, ownerhold.ReleasedAsked)
		}

		return nil
	case nodeproto.DeviceViewReset:
		return v.send(ctx, nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewReset})
	}

	in, ok := input(a)
	if !ok {
		return nil
	}

	if !v.p.hold.Input(now) {
		select {
		case v.again <- struct{}{}:
		default:
		}

		return nil
	}

	return v.send(ctx, in)
}

// input is the owner's input as the node takes it, only the fields its type
// uses; false when it is not one.
func input(a Action) (nodeproto.DeviceViewMessage, bool) {
	switch a.Type {
	case nodeproto.DeviceViewTouch:
		unit := func(f float64) bool { return !math.IsNaN(f) && f >= 0 && f <= 1 }
		if (a.Action != "down" && a.Action != "move" && a.Action != "up") || !unit(a.X) || !unit(a.Y) {
			return nodeproto.DeviceViewMessage{}, false
		}

		return nodeproto.DeviceViewMessage{Type: a.Type, Action: a.Action, X: a.X, Y: a.Y}, true
	case nodeproto.DeviceViewKey:
		if a.Keycode <= 0 || a.Keycode >= maxKeycode {
			return nodeproto.DeviceViewMessage{}, false
		}

		return nodeproto.DeviceViewMessage{Type: a.Type, Keycode: a.Keycode}, true
	case nodeproto.DeviceViewText:
		if a.Text == "" {
			return nodeproto.DeviceViewMessage{}, false
		}

		return nodeproto.DeviceViewMessage{Type: a.Type, Text: a.Text}, true
	case nodeproto.DeviceViewBack, nodeproto.DeviceViewHome, nodeproto.DeviceViewRecents, nodeproto.DeviceViewPower:
		return nodeproto.DeviceViewMessage{Type: a.Type}, true
	}

	return nodeproto.DeviceViewMessage{}, false
}

// send passes a message to the node; one that comes before the view is
// connected is dropped.
func (v *View) send(ctx context.Context, in nodeproto.DeviceViewMessage) error {
	v.mu.Lock()
	end := v.end
	v.mu.Unlock()

	if end == nil {
		return nil
	}

	return end.Send(ctx, in)
}

// Close ends the view. clean says the owner closed it, which gives the phone
// back at once; a view that dropped keeps the hold for a while.
func (v *View) Close(ctx context.Context, clean bool) {
	v.closing.Do(func() {
		v.stop()

		v.mu.Lock()
		end := v.end
		v.mu.Unlock()

		if end != nil {
			end.Close("the owner's view closed")
		}

		if reason := v.p.hold.Closed(clean, v.p.cfg.Clock.Now()); reason != "" {
			v.p.released(ctx, reason)
		}

		v.p.busy()
	})
}
