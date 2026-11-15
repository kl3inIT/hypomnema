package device

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// fakeStream is a scrcpy stream the test plays: the packets it is given, and
// the control messages it was sent.
type fakeStream struct {
	size    screenSize
	packets chan packet

	mu      sync.Mutex
	sent    [][]byte
	closed  bool
	closing chan struct{}
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		size: screenSize{Width: 720, Height: 1280}, packets: make(chan packet, 64), closing: make(chan struct{}),
	}
}

func (s *fakeStream) Size() screenSize { return s.size }

func (s *fakeStream) Read() (packet, error) {
	select {
	case p := <-s.packets:
		return p, nil
	case <-s.closing:
		return packet{}, errors.New("closed")
	}
}

func (s *fakeStream) Control(message []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sent = append(s.sent, slices.Clone(message))

	return nil
}

func (s *fakeStream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed {
		s.closed = true
		close(s.closing)
	}
}

func (s *fakeStream) controls() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.sent)
}

func (s *fakeStream) ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// viewRig is a rig whose phone streams the test's fake streams, one for each
// time the stream is started.
type viewRig struct {
	*rig
	streams chan *fakeStream
	opened  chan *fakeStream
}

func newViewRig(t *testing.T) *viewRig {
	t.Helper()

	r := &viewRig{rig: newRig(t), streams: make(chan *fakeStream, 4), opened: make(chan *fakeStream, 4)}
	r.m.screenSource = func(ctx context.Context) (screenStream, error) {
		select {
		case s := <-r.streams:
			r.opened <- s

			return s, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return r
}

// next is what a view says next, within five seconds: the fake install takes a while on a busy machine.
func next(t *testing.T, v *View) ([]byte, *nodeproto.DeviceViewMessage) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	frame, said, err := v.Next(ctx)
	require.NoError(t, err)

	return frame, said
}

// until reads what a view says until a message of the type, or a frame when
// typ is empty, and returns it.
func until(t *testing.T, v *View, typ string) ([]byte, *nodeproto.DeviceViewMessage) {
	t.Helper()

	for {
		frame, said := next(t, v)
		if typ == "" && frame != nil || said != nil && said.Type == typ {
			return frame, said
		}
	}
}

var (
	config = packet{Config: true, Data: []byte{0, 0, 0, 1, 0x67}}
	key    = packet{Key: true, PTS: 1, Data: []byte{0, 0, 0, 1, 0x65}}
	delta  = packet{PTS: 2, Data: []byte{0, 0, 0, 1, 0x41}}
)

// Scenario: the owner opens the phone's view while the phone is stopped. The
// view starts it and says its state as it goes; once it runs the screen's
// stream starts, the view is told the screen's size, and its first frame is a
// key frame with the codec's configuration before it.
func TestAViewStartsAStoppedPhoneAndShowsItsScreen(t *testing.T) {
	t.Parallel()

	r := newViewRig(t)
	stream := newFakeStream()
	r.streams <- stream

	v := r.m.Watch(t.Context())
	t.Cleanup(v.Close)

	// The states as they come; one that changed again before it was read is said as it is then.
	_, said := next(t, v)
	assert.Equal(t, nodeproto.DeviceViewState, said.Type)

	for said.State != nodeproto.DeviceRunning {
		_, said = until(t, v, nodeproto.DeviceViewState)
	}

	_, said = until(t, v, nodeproto.DeviceViewScreen)
	assert.Equal(t, &nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewScreen, Width: 720, Height: 1280}, said)

	// A frame before the first key frame cannot be decoded: it is not given.
	stream.packets <- delta
	stream.packets <- config
	stream.packets <- key

	frame, _ := until(t, v, "")
	assert.Equal(t, semuFrame(key, config.Data), frame)
}

// Scenario: a second view joins while the stream runs. It is told the
// screen's size, the server is asked for a key frame at once (serve-emu's
// reset on a client's open), and the frames before that key frame are not
// given to it; the first view goes on getting every frame.
func TestAViewThatJoinsLateStartsAtTheNextKeyFrame(t *testing.T) {
	t.Parallel()

	r := newViewRig(t)
	r.start(t)

	stream := newFakeStream()
	r.streams <- stream

	first := r.m.Watch(t.Context())
	t.Cleanup(first.Close)

	<-r.opened

	stream.packets <- config
	stream.packets <- key

	frame, _ := until(t, first, "")
	assert.Equal(t, semuFrame(key, config.Data), frame)

	second := r.m.Watch(t.Context())
	t.Cleanup(second.Close)

	_, said := until(t, second, nodeproto.DeviceViewScreen)
	assert.Equal(t, 720, said.Width)
	assert.Contains(t, stream.controls(), resetVideoMessage())

	stream.packets <- delta
	stream.packets <- key

	frame, _ = until(t, first, "")
	assert.Equal(t, semuFrame(delta, nil), frame)

	frame, _ = until(t, second, "")
	assert.Equal(t, semuFrame(key, config.Data), frame, "the late view's first frame is the key frame")
}

// A view that falls behind drops frames until a key frame and the server is
// asked for one, as serve-emu's slow client; the view that keeps up misses
// nothing.
func TestAViewThatFallsBehindDropsUntilAKeyFrame(t *testing.T) {
	t.Parallel()

	r := newViewRig(t)
	r.start(t)

	stream := newFakeStream()
	r.streams <- stream

	slow := r.m.Watch(t.Context())
	t.Cleanup(slow.Close)

	<-r.opened

	until(t, slow, nodeproto.DeviceViewScreen)

	stream.packets <- config
	stream.packets <- key

	// The slow view takes nothing for a while: its queue fills.
	for range viewQueue + 3 {
		stream.packets <- delta
	}

	r.clock.advance(time.Second)

	require.Eventually(t, func() bool {
		slow.mu.Lock()
		defer slow.mu.Unlock()

		return slow.waitKey && len(slow.frames) == viewQueue
	}, time.Second, time.Millisecond)

	resets := func() int {
		n := 0

		for _, c := range stream.controls() {
			if slices.Equal(c, resetVideoMessage()) {
				n++
			}
		}

		return n
	}
	assert.Equal(t, 1, resets(), "the server was asked for a key frame")

	// The queue drains; the frames after it wait for a key frame.
	for range viewQueue {
		until(t, slow, "")
	}

	stream.packets <- delta
	stream.packets <- key

	frame, _ := until(t, slow, "")
	assert.Equal(t, semuFrame(key, config.Data), frame)
}

// The owner's input goes to the phone as scrcpy's control messages: a touch
// at a point from 0 to 1 in the video's pixels, a text, Back, and the Home
// key down and up.
func TestTheOwnersInputGoesOnTheControlSocket(t *testing.T) {
	t.Parallel()

	r := newViewRig(t)
	r.start(t)

	stream := newFakeStream()
	r.streams <- stream

	v := r.m.Watch(t.Context())
	t.Cleanup(v.Close)

	<-r.opened
	until(t, v, nodeproto.DeviceViewScreen)

	for _, in := range []nodeproto.DeviceViewMessage{
		{Type: nodeproto.DeviceViewTouch, Action: "down", X: 0.5, Y: 0.25},
		{Type: nodeproto.DeviceViewTouch, Action: "up", X: 0.5, Y: 0.25},
		{Type: nodeproto.DeviceViewText, Text: "mật khẩu"},
		{Type: nodeproto.DeviceViewBack},
		{Type: nodeproto.DeviceViewHome},
		{Type: nodeproto.DeviceViewTouch, Action: "wave"},
	} {
		require.NoError(t, v.Input(in))
	}

	size := screenSize{Width: 720, Height: 1280}
	assert.Equal(t, [][]byte{
		touchMessage(actionDown, 360, 320, size), touchMessage(actionUp, 360, 320, size),
		textMessages("mật khẩu")[0], backMessage(actionDown), backMessage(actionUp),
		keyMessage(actionDown, keycodeHome), keyMessage(actionUp, keycodeHome),
	}, stream.controls())
}

// The phone is not stopped as idle while a view is open, however long; once
// the last view closed, the stream ends and the idle time counts from then.
func TestAnOpenViewKeepsThePhoneFromItsIdleStop(t *testing.T) {
	t.Parallel()

	r := newViewRig(t)
	r.start(t)

	stream := newFakeStream()
	r.streams <- stream

	v := r.m.Watch(t.Context())

	<-r.opened

	r.clock.advance(IdleAfter + time.Minute)
	r.m.Sweep(t.Context())
	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State)

	v.Close()
	require.Eventually(t, stream.ended, time.Second, time.Millisecond)

	r.clock.advance(IdleAfter - time.Minute)
	r.m.Sweep(t.Context())
	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State)

	r.clock.advance(2 * time.Minute)
	r.m.Sweep(t.Context())
	assert.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)

	_, _, err := v.Next(t.Context())
	require.ErrorIs(t, err, ErrViewClosed)
}

// A stream that ends while a view is open, as when the phone is stopped, is
// started again once the phone runs again, and the view is told the state on
// the way.
func TestAStreamThatEndedStartsAgainWhenThePhoneRuns(t *testing.T) {
	t.Parallel()

	r := newViewRig(t)
	r.start(t)

	first := newFakeStream()
	r.streams <- first

	v := r.m.Watch(t.Context())
	t.Cleanup(v.Close)

	<-r.opened

	r.m.Stop(t.Context())
	first.Close()

	_, said := until(t, v, nodeproto.DeviceViewState)
	assert.Equal(t, nodeproto.DeviceStopped, said.State)

	second := newFakeStream()
	r.streams <- second
	r.start(t)

	select {
	case opened := <-r.opened:
		assert.Same(t, second, opened)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not start again")
	}
}
