package phone

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/ownerhold"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// fakeEnd is the node's side of a view: what the test has the node say, and
// what the view sent it.
type fakeEnd struct {
	says chan nodeproto.DeviceViewMessage

	mu     sync.Mutex
	sent   []nodeproto.DeviceViewMessage
	closed bool
	done   chan struct{}
}

func newEnd() *fakeEnd {
	return &fakeEnd{says: make(chan nodeproto.DeviceViewMessage, 8), done: make(chan struct{})}
}

func (e *fakeEnd) Read(ctx context.Context) ([]byte, *nodeproto.DeviceViewMessage, error) {
	select {
	case m := <-e.says:
		if m.Type == "frame" {
			return []byte(m.Text), nil, nil
		}

		return nil, &m, nil
	case <-e.done:
		return nil, nil, errors.New("closed")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (e *fakeEnd) Send(_ context.Context, in nodeproto.DeviceViewMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.sent = append(e.sent, in)

	return nil
}

func (e *fakeEnd) Close(string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.closed {
		e.closed = true
		close(e.done)
	}
}

func (e *fakeEnd) got() []nodeproto.DeviceViewMessage {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]nodeproto.DeviceViewMessage(nil), e.sent...)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) pass(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// viewPhone is a Phone whose node opens the fake end given, and the clock.
func viewPhone(link *fakeLink, machines Machines, end *fakeEnd, n orchestration.Node) (*Phone, *clock) {
	c := &clock{now: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)}

	return New(Config{
		Nodes: &fakeNodes{nodes: []orchestration.Node{n}}, Link: link, Machines: machines, Clock: c,
		Views:      func(context.Context, string) (ViewEnd, error) { return end, nil },
		WakeWithin: 300 * time.Millisecond, Poll: 5 * time.Millisecond,
	}), c
}

func nextUp(t *testing.T, v *View) Update {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	up, err := v.Next(ctx)
	require.NoError(t, err)

	return up
}

// Scenario: the owner opens the phone's view while its machine is stopped.
// The view says the agents control, starts the machine and says the phone is
// waking, and once the node said hello passes on what the node says: the
// phone's state and its frames.
func TestAViewWakesAStoppedMachineAndPassesTheScreenOn(t *testing.T) {
	t.Parallel()

	link := newLink()
	machines := &fakeMachines{link: link, connect: true}
	end := newEnd()
	p, _ := viewPhone(link, machines, end, phoneNode(orchestration.MachineStopped))

	v, err := p.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { v.Close(context.Background()) })

	assert.Equal(t, ControllerAgent, nextUp(t, v).Controller)
	assert.Equal(t, StateWaking, nextUp(t, v).Said.State)

	end.says <- nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewState, State: nodeproto.DeviceStarting}
	end.says <- nodeproto.DeviceViewMessage{Type: "frame", Text: "SEMU-key"}

	assert.Equal(t, nodeproto.DeviceStarting, nextUp(t, v).Said.State)
	assert.Equal(t, []byte("SEMU-key"), nextUp(t, v).Frame)
	assert.Equal(t, []string{"n-phone"}, machines.started)

	// A second view while the machine is still starting does not start it again.
	second, err := p.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { second.Close(context.Background()) })
	assert.Equal(t, []string{"n-phone"}, machines.started)
}

// Scenario: the owner takes the phone over. Input before taking control is
// dropped and the page is told again who controls; with control, a touch, a
// key and a text reach the node with only their own fields; an agent's call
// meanwhile is answered that the phone's tools are paused, and goes through
// again once the owner hands control back.
func TestTheOwnerTakesThePhoneAndAgentsArePaused(t *testing.T) {
	t.Parallel()

	link := newLink()
	link.connect("n-phone")

	end := newEnd()
	p, _ := viewPhone(link, nil, end, phoneNode(""))

	v, err := p.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { v.Close(context.Background()) })

	assert.Equal(t, ControllerAgent, nextUp(t, v).Controller)

	// The view connects on its own; a reset is passed on once it has.
	require.Eventually(t, func() bool {
		_ = v.Do(t.Context(), Action{Type: nodeproto.DeviceViewReset})

		return len(end.got()) > 0
	}, time.Second, 5*time.Millisecond)

	require.NoError(t, v.Do(t.Context(), Action{Type: nodeproto.DeviceViewTouch, Action: "down", X: 0.5, Y: 0.5}))
	assert.Equal(t, ControllerAgent, nextUp(t, v).Controller, "a dropped input says again who controls")

	require.NoError(t, v.Do(t.Context(), Action{Type: "takeControl"}))
	assert.Equal(t, ControllerYou, nextUp(t, v).Controller)
	assert.True(t, p.Controlled())

	for _, a := range []Action{
		{Type: nodeproto.DeviceViewTouch, Action: "down", X: 0.25, Y: 0.75, Text: "ignored"},
		{Type: nodeproto.DeviceViewTouch, Action: "down", X: 1.5, Y: 0.5},
		{Type: nodeproto.DeviceViewKey, Keycode: 66},
		{Type: nodeproto.DeviceViewKey, Keycode: 9000},
		{Type: nodeproto.DeviceViewText, Text: "mật khẩu"},
		{Type: nodeproto.DeviceViewHome, Keycode: 3},
		{Type: "state", State: "running"},
	} {
		require.NoError(t, v.Do(t.Context(), a))
	}

	assert.Equal(t, []nodeproto.DeviceViewMessage{
		{Type: nodeproto.DeviceViewTouch, Action: "down", X: 0.25, Y: 0.75},
		{Type: nodeproto.DeviceViewKey, Keycode: 66},
		{Type: nodeproto.DeviceViewText, Text: "mật khẩu"},
		{Type: nodeproto.DeviceViewHome},
	}, end.got()[len(end.got())-4:])

	raw, err := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
	require.NoError(t, err)

	var answer nodeproto.DeviceAnswer
	require.NoError(t, json.Unmarshal(raw, &answer))
	assert.True(t, answer.IsError)
	assert.Equal(t, Refusal, answer.Content[0].Text)
	assert.Empty(t, link.calls, "a paused call does not reach the phone")

	require.NoError(t, v.Do(t.Context(), Action{Type: "releaseControl"}))
	assert.Equal(t, ControllerAgent, nextUp(t, v).Controller)

	_, err = p.Call(t.Context(), "d-1", devicetool.Screen, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n-phone d-1 device_screen"}, link.calls)
}

// A call that was running when the owner took the phone is answered as
// paused, not with what it saw: the owner may have typed meanwhile.
func TestACallRunningWhenTheOwnerTakesThePhoneIsAnsweredPaused(t *testing.T) {
	t.Parallel()

	link := newLink()
	link.connect("n-phone")
	link.hold = make(chan struct{})

	p, _ := viewPhone(link, nil, newEnd(), phoneNode(""))

	answered := make(chan json.RawMessage, 1)

	go func() {
		raw, _ := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
		answered <- raw
	}()

	<-link.entered

	v, err := p.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { v.Close(context.Background()) })

	taken := make(chan struct{})

	go func() {
		_ = v.Do(t.Context(), Action{Type: "takeControl"})
		close(taken)
	}()

	require.Eventually(t, p.Controlled, time.Second, time.Millisecond)
	close(link.hold)
	<-taken

	var answer nodeproto.DeviceAnswer
	require.NoError(t, json.Unmarshal(<-answered, &answer))
	assert.Equal(t, Refusal, answer.Content[0].Text)
}

// The browser's release rules: handing back gives the phone back at once; the
// last view going, closed by the page or dropped, keeps the hold for the
// grace, and a view opened within it carries the hold on (issue #603); and a
// hold the owner sends nothing in goes back after the idle time.
func TestTheHoldEndsAsTheBrowsers(t *testing.T) {
	t.Parallel()

	link := newLink()
	link.connect("n-phone")

	take := func(t *testing.T) (*Phone, *clock, *View) {
		t.Helper()

		p, c := viewPhone(link, nil, newEnd(), phoneNode(""))
		v, err := p.Watch(t.Context())
		require.NoError(t, err)
		require.NoError(t, v.Do(t.Context(), Action{Type: "takeControl"}))
		require.True(t, p.Controlled())

		return p, c, v
	}

	p, _, v := take(t)
	require.NoError(t, v.Do(t.Context(), Action{Type: "releaseControl"}))
	assert.False(t, p.Controlled(), "handing back gives the phone back at once")
	v.Close(t.Context())

	p, c, v := take(t)
	v.Close(t.Context())
	assert.True(t, p.Controlled(), "a view the page closed keeps the hold for the grace")
	c.pass(ownerhold.Grace - time.Second)
	assert.Empty(t, p.hold.Expire(c.Now()))

	next, err := p.Watch(t.Context())
	require.NoError(t, err)
	c.pass(2 * time.Second)
	assert.Empty(t, p.hold.Expire(c.Now()), "a view opened within the grace carries the hold on")
	next.Close(t.Context())
	c.pass(ownerhold.Grace)
	assert.Equal(t, ownerhold.ReleasedGone, p.hold.Expire(c.Now()))

	p, c, v = take(t)
	t.Cleanup(func() { v.Close(context.Background()) })
	c.pass(ownerhold.Idle)
	assert.Equal(t, ownerhold.ReleasedIdle, p.hold.Expire(c.Now()))
	assert.False(t, p.Controlled())
}

// At most four views are open, as the browser's; a control plane where no
// node holds a phone has none to show.
func TestViewsAreBounded(t *testing.T) {
	t.Parallel()

	link := newLink()
	link.connect("n-phone")
	p, _ := viewPhone(link, nil, newEnd(), phoneNode(""))

	for range maxViews {
		v, err := p.Watch(t.Context())
		require.NoError(t, err)
		t.Cleanup(func() { v.Close(context.Background()) })
	}

	_, err := p.Watch(t.Context())
	require.ErrorIs(t, err, ErrTooManyViews)

	none, _ := viewPhone(link, nil, newEnd(), orchestration.Node{ID: "n-build"})
	_, err = none.Watch(t.Context())
	require.ErrorIs(t, err, ErrUnavailable)
}

// Scenario: the owner's view opens while the phone's node restarts, for its
// own update (issue #545): the node is offline though its machine is on. The
// view waits for the node's hello, does not start the machine, and connects
// once the node is back; a node that does not come back within the wait ends
// the view as unavailable.
func TestAViewWaitsForANodeThatRestarts(t *testing.T) {
	t.Parallel()

	link := newLink()
	machines := &fakeMachines{link: link}
	end := newEnd()
	p, _ := viewPhone(link, machines, end, phoneNode(""))

	v, err := p.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { v.Close(context.Background()) })

	assert.Equal(t, ControllerAgent, nextUp(t, v).Controller)

	time.Sleep(50 * time.Millisecond)
	link.connect("n-phone")

	end.says <- nodeproto.DeviceViewMessage{Type: nodeproto.DeviceViewState, State: nodeproto.DeviceStarting}
	assert.Equal(t, nodeproto.DeviceStarting, nextUp(t, v).Said.State)
	assert.Empty(t, machines.started, "a machine that is on is not started")

	gone, _ := viewPhone(newLink(), machines, newEnd(), phoneNode(""))
	lost, err := gone.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { lost.Close(context.Background()) })

	assert.Equal(t, ControllerAgent, nextUp(t, lost).Controller)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err = lost.Next(ctx)
	require.ErrorIs(t, err, ErrUnavailable)
}
