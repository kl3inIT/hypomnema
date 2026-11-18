package device

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The owner's view of the phone's screen (issue #417, step 3). One scrcpy
// stream serves every view open, as serve-emu serves its clients
// (src/middleware.ts): the codec's configuration (SPS and PPS) is kept and
// put before every key frame, a view that joins waits for the next key frame
// and the server is asked for one at once, and a view that falls behind drops
// frames until a key frame, asking for one. The stream starts with the first
// view and ends with the last, as Orca's pane holds its scrcpy session.
// Viewing starts a phone that is not running and puts its idle stop off for as
// long as a view is open (the design's "15 minutes without a call or a
// view").

const (
	// viewQueue is how many frames may wait for a view; one more drops frames until a key frame. About two
	// seconds at the trial machine's 12 fps.
	viewQueue = 24
	// resetEvery bounds how often the server is asked for a key frame.
	resetEvery = 500 * time.Millisecond
	// reopenAfter is the wait before a stream that ended is started again while a view is open.
	reopenAfter = 3 * time.Second
	// DeviceViewError is the type of the text message that says the stream could not start.
	DeviceViewError = "error"
)

// ErrViewClosed is what a closed view's Next returns.
var ErrViewClosed = errors.New("the view was closed")

// screen is the stream the views share. Its fields are guarded by mu.
type screen struct {
	mu      sync.Mutex
	views   map[*View]struct{}
	serving bool
	// filming keeps the stream open while a take runs, with no view open (take.go).
	filming bool
	stream  screenStream
	config  []byte
	size    screenSize
	reset   time.Time
	// wake is told when the phone's state changed or a view left, for serve.
	wake chan struct{}
}

// View is one view of the phone's screen.
type View struct {
	m      *Manager
	frames chan []byte
	notify chan struct{}
	done   chan struct{}

	mu sync.Mutex
	// state and size wait to be said; waitKey drops frames until a key frame.
	state   *nodeproto.DeviceViewMessage
	size    *nodeproto.DeviceViewMessage
	failure *nodeproto.DeviceViewMessage
	waitKey bool

	closing sync.Once
}

// Watch opens a view of the phone's screen. A phone that is not running is
// started, under ctx; the view says its state as it goes.
func (m *Manager) Watch(ctx context.Context) *View {
	v := &View{
		m: m, frames: make(chan []byte, viewQueue), notify: make(chan struct{}, 1), done: make(chan struct{}),
		waitKey: true,
	}

	s := &m.live
	s.mu.Lock()

	if s.views == nil {
		s.views = map[*View]struct{}{}
		s.wake = make(chan struct{}, 1)
	}

	s.views[v] = struct{}{}

	// The state is read once the view is in: a change from now on reaches it too. The lock order is live.mu, then
	// the Manager's mu, which set releases before it tells the views.
	st := m.Status()
	v.state = &nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewState, State: st.State, Reason: st.Reason}

	if s.stream != nil {
		v.size = &nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewScreen, Width: s.size.Width, Height: s.size.Height}
	}

	serve := !s.serving
	s.serving = true
	stream := s.stream
	s.mu.Unlock()

	m.Touch()

	switch st.State {
	case nodeproto.DeviceRunning, nodeproto.DeviceStarting, nodeproto.DeviceInstalling:
	default:
		m.Ask(context.WithoutCancel(ctx), true)
	}

	if serve {
		go m.serveScreen(context.WithoutCancel(ctx))
	}

	// A view that joins a stream under way asks for a key frame now rather than at the encoder's next.
	if stream != nil {
		m.askKeyFrame(stream)
	}

	return v
}

// watched says whether a view is open or a take runs, which puts the idle
// stop off: a take is a use, as a view is (issue #817, row 5).
func (s *screen) watched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.views) > 0 || s.filming
}

// used says whether the stream is wanted: a view is open or a take runs. The
// caller holds mu.
func (s *screen) used() bool { return len(s.views) > 0 || s.filming }

// stateChanged tells every view the phone's state, and serve that it changed.
func (s *screen) stateChanged(state, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for v := range s.views {
		v.say(func() {
			v.state = &nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewState, State: state, Reason: reason}
		})
	}

	s.poke()
}

// poke wakes serve. The caller holds mu.
func (s *screen) poke() {
	if s.wake == nil {
		return
	}

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// serveScreen runs the stream while a view is open and the phone runs, and
// starts it again after a pause when it ended with a view still open.
func (m *Manager) serveScreen(ctx context.Context) {
	s := &m.live

	for {
		s.mu.Lock()
		if !s.used() {
			s.serving = false
			s.mu.Unlock()

			return
		}

		wake := s.wake
		s.mu.Unlock()

		if m.Status().State != nodeproto.DeviceRunning {
			select {
			case <-wake:
			case <-ctx.Done():
				m.endScreen()

				return
			}

			continue
		}

		stream, err := m.openStream(ctx)
		if err != nil {
			m.opts.Logger.WarnContext(ctx, "the phone's screen could not be streamed",
				slog.String("event", "device.view_failed"), slog.String("error", err.Error()))
			s.failed(err)
			m.endRunning(devicetool.EndedStreamError, "the phone's screen could not be streamed: "+err.Error())
		} else {
			m.relay(stream)
		}

		select {
		case <-ctx.Done():
			m.endScreen()

			return
		case <-time.After(m.opts.Poll + reopenAfter):
		case <-wake:
		}
	}
}

// openStream starts the stream, by the test's source when it has one.
func (m *Manager) openStream(ctx context.Context) (screenStream, error) {
	if m.screenSource != nil {
		return m.screenSource(ctx)
	}

	return m.openScrcpy(ctx)
}

// failed tells every view the stream did not start.
func (s *screen) failed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for v := range s.views {
		v.say(func() {
			v.failure = &nodeproto.DeviceViewMessage{Type: DeviceViewError, Reason: err.Error()}
		})
	}
}

// relay hands the stream's frames to the views until it ends.
func (m *Manager) relay(stream screenStream) {
	s := &m.live

	s.mu.Lock()

	// The last view left, and no take runs, while the stream was starting: it ends at once.
	if !s.used() {
		s.mu.Unlock()
		stream.Close()

		return
	}

	s.stream, s.config, s.size = stream, nil, stream.Size()

	for v := range s.views {
		v.say(func() {
			v.failure = nil
			v.waitKey = true
			v.size = &nodeproto.DeviceViewMessage{
				Type: nodeproto.DeviceViewScreen, Width: s.size.Width, Height: s.size.Height,
			}
		})
	}

	s.mu.Unlock()

	// A take's segment ends with the stream before: the new stream starts with its own configuration.
	m.streamRestarted()

	defer func() {
		s.mu.Lock()
		s.stream = nil
		s.mu.Unlock()
		stream.Close()
	}()

	for {
		p, err := stream.Read()
		if err != nil {
			return
		}

		// A take reads the packet first, through its own queue: the views' fan-out may drop it.
		ask := m.feed(p)

		if m.fanOut(p) || ask {
			m.askKeyFrame(stream)
		}

		if m.fed != nil {
			m.fed(p)
		}
	}
}

// fanOut gives one packet to every view, and says whether one fell behind
// and a key frame is to be asked for.
func (m *Manager) fanOut(p packet) bool {
	s := &m.live

	s.mu.Lock()
	defer s.mu.Unlock()

	if p.Config {
		s.config = p.Data

		// After the phone turned, the size is the new SPS's: touches are sized by it, and each view is told.
		if size, ok := spsSize(p.Data); ok && size != s.size {
			s.size = size

			for v := range s.views {
				v.say(func() {
					v.waitKey = true
					v.size = &nodeproto.DeviceViewMessage{
						Type: nodeproto.DeviceViewScreen, Width: size.Width, Height: size.Height,
					}
				})
			}
		}

		return false
	}

	var out []byte

	behind := false

	for v := range s.views {
		if out == nil {
			out = semuFrame(p, s.config)
		}

		if !v.offer(out, p.Key) {
			behind = true
		}
	}

	return behind
}

// askKeyFrame asks the server for a key frame, at most every resetEvery.
// The write is made outside the lock: a slow control socket holds up no view.
func (m *Manager) askKeyFrame(stream screenStream) {
	m.live.mu.Lock()
	now := m.opts.Clock.Now()
	due := now.Sub(m.live.reset) >= resetEvery

	if due {
		m.live.reset = now
	}
	m.live.mu.Unlock()

	if due {
		_ = stream.Control(resetVideoMessage())
	}
}

// streamFor keeps the stream open for a take (on), or lets it end with the
// last view once no take runs. A take that starts asks for a key frame.
func (m *Manager) streamFor(on bool) {
	s := &m.live

	if !on {
		on = m.filmingNow()
	}

	s.mu.Lock()

	if s.views == nil {
		s.views = map[*View]struct{}{}
		s.wake = make(chan struct{}, 1)
	}

	s.filming = on
	serve := on && !s.serving
	if serve {
		s.serving = true
	}

	stream := s.stream
	idle := !s.used()
	s.poke()
	s.mu.Unlock()

	switch {
	case serve:
		go m.serveScreen(context.Background())
	case on && stream != nil:
		m.askKeyFrame(stream)
	case idle && stream != nil:
		stream.Close()
	}
}

// askKey asks the stream running, if any, for a key frame.
func (m *Manager) askKey() {
	m.live.mu.Lock()
	stream := m.live.stream
	m.live.mu.Unlock()

	if stream != nil {
		m.askKeyFrame(stream)
	}
}

// endScreen ends the stream and forgets the views: the node is stopping.
func (m *Manager) endScreen() {
	s := &m.live

	s.mu.Lock()
	stream := s.stream
	s.serving = false
	s.mu.Unlock()

	if stream != nil {
		stream.Close()
	}
}

// say changes what the view waits to say and wakes its Next. The caller holds
// the screen's mu.
func (v *View) say(change func()) {
	v.mu.Lock()
	change()
	v.mu.Unlock()

	select {
	case v.notify <- struct{}{}:
	default:
	}
}

// offer queues a frame; false when the view is behind and dropped it. A view
// waiting for a key frame drops the others.
func (v *View) offer(frame []byte, key bool) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.waitKey && !key {
		return true
	}

	select {
	case v.frames <- frame:
		v.waitKey = false

		return true
	default:
		v.waitKey = true

		return false
	}
}

// Next returns what the view says next: a frame, or a text message. It
// fails once the view is closed.
func (v *View) Next(ctx context.Context) ([]byte, *nodeproto.DeviceViewMessage, error) {
	for {
		select {
		case <-v.done:
			return nil, nil, ErrViewClosed
		default:
		}

		v.mu.Lock()

		for _, pending := range []**nodeproto.DeviceViewMessage{&v.state, &v.failure, &v.size} {
			if *pending != nil {
				said := *pending
				*pending = nil
				v.mu.Unlock()

				return nil, said, nil
			}
		}

		v.mu.Unlock()

		select {
		case frame := <-v.frames:
			return frame, nil, nil
		case <-v.notify:
		case <-v.done:
			return nil, nil, ErrViewClosed
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// Input carries out one of the owner's input messages on the phone. One
// that comes while no stream runs is dropped.
func (v *View) Input(in nodeproto.DeviceViewMessage) error {
	s := &v.m.live

	s.mu.Lock()
	stream, size := s.stream, s.size
	s.mu.Unlock()

	if stream == nil {
		return nil
	}

	v.m.Touch()

	switch in.Type {
	case nodeproto.DeviceViewReset:
		v.m.askKeyFrame(stream)

		return nil
	case nodeproto.DeviceViewTouch:
		action, ok := map[string]byte{"down": actionDown, "move": actionMove, "up": actionUp}[in.Action]
		if !ok || size.Width < 1 || size.Height < 1 {
			return nil
		}

		return stream.Control(touchMessage(action, toPixel(in.X, size.Width), toPixel(in.Y, size.Height), size))
	case nodeproto.DeviceViewText:
		for _, message := range textMessages(in.Text) {
			if err := stream.Control(message); err != nil {
				return err
			}
		}

		return nil
	case nodeproto.DeviceViewBack:
		if err := stream.Control(backMessage(actionDown)); err != nil {
			return err
		}

		return stream.Control(backMessage(actionUp))
	}

	keycode, ok := map[string]int{
		nodeproto.DeviceViewKey: in.Keycode, nodeproto.DeviceViewHome: keycodeHome,
		nodeproto.DeviceViewRecents: keycodeRecents, nodeproto.DeviceViewPower: keycodePower,
	}[in.Type]
	if !ok || keycode <= 0 {
		return nil
	}

	if err := stream.Control(keyMessage(actionDown, keycode)); err != nil {
		return err
	}

	return stream.Control(keyMessage(actionUp, keycode))
}

// Close ends the view. The last view's close ends the stream, and the phone's
// idle time counts from it.
func (v *View) Close() {
	v.closing.Do(func() {
		close(v.done)

		s := &v.m.live

		s.mu.Lock()
		delete(s.views, v)
		last := !s.used()
		stream := s.stream
		s.poke()
		s.mu.Unlock()

		v.m.Touch()

		if last && stream != nil {
			stream.Close()
		}
	})
}
