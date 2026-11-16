package phone

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// idleRig is a phone whose machine the control plane made and is on, its node
// connected and its phone stopped, with the clock.
func idleRig(t *testing.T) (*Phone, *fakeLink, *fakeMachines, *clock) {
	t.Helper()

	link := newLink()
	link.connect("n-phone")
	link.said("n-phone", nodeproto.DeviceStopped)

	n := phoneNode("")
	n.Machine.ResourceID = "i-0123"

	machines := &fakeMachines{link: link}
	p, c := viewPhone(link, machines, newEnd(), n)

	return p, link, machines, c
}

// The machine is stopped MachineIdle after a sweep first found it idle, and
// not before.
func TestAnIdleMachineIsStoppedAfterItsIdleTime(t *testing.T) {
	t.Parallel()

	p, _, machines, c := idleRig(t)

	p.Sweep(t.Context())
	c.pass(MachineIdle - time.Minute)
	p.Sweep(t.Context())
	assert.Empty(t, machines.stopped)

	c.pass(time.Minute)
	p.Sweep(t.Context())
	assert.Equal(t, []string{"n-phone"}, machines.stopped)
}

// A running phone, a call and a view each start the idle time again.
func TestUseStartsTheIdleTimeAgain(t *testing.T) {
	t.Parallel()

	t.Run("a running phone", func(t *testing.T) {
		t.Parallel()

		p, link, machines, c := idleRig(t)

		p.Sweep(t.Context())
		c.pass(MachineIdle - time.Minute)
		link.said("n-phone", nodeproto.DeviceRunning)
		p.Sweep(t.Context())

		// The phone stopped just after that sweep: its idle time is counted from the next.
		link.said("n-phone", nodeproto.DeviceStopped)
		c.pass(time.Minute)
		p.Sweep(t.Context())
		c.pass(MachineIdle - time.Second)
		p.Sweep(t.Context())
		assert.Empty(t, machines.stopped)

		c.pass(time.Second)
		p.Sweep(t.Context())
		assert.Equal(t, []string{"n-phone"}, machines.stopped)
	})

	t.Run("a call", func(t *testing.T) {
		t.Parallel()

		p, _, machines, c := idleRig(t)

		p.Sweep(t.Context())
		c.pass(MachineIdle - time.Minute)
		_, err := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
		require.NoError(t, err)

		c.pass(time.Minute)
		p.Sweep(t.Context())
		assert.Empty(t, machines.stopped)
	})

	t.Run("a view", func(t *testing.T) {
		t.Parallel()

		p, _, machines, c := idleRig(t)

		p.Sweep(t.Context())
		c.pass(MachineIdle - time.Minute)
		v, err := p.Watch(t.Context())
		require.NoError(t, err)

		c.pass(time.Hour)
		p.Sweep(t.Context())
		assert.Empty(t, machines.stopped, "an open view keeps the machine on")

		v.Close(t.Context())
		c.pass(time.Minute)
		p.Sweep(t.Context())
		assert.Empty(t, machines.stopped, "the idle time starts at the view's close")
	})
}

// A call in flight keeps the machine on, however long it runs.
func TestACallInFlightKeepsTheMachineOn(t *testing.T) {
	t.Parallel()

	p, link, machines, c := idleRig(t)
	link.hold = make(chan struct{})

	done := make(chan error, 1)

	go func() {
		_, err := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
		done <- err
	}()

	require.Equal(t, "d-1", <-link.entered)

	p.Sweep(t.Context())
	c.pass(MachineIdle + time.Minute)
	p.Sweep(t.Context())
	assert.Empty(t, machines.stopped)

	close(link.hold)
	require.NoError(t, <-done)
}

// A stop that failed is asked again at the next sweep, without waiting out
// the idle time again.
func TestAFailedStopIsAskedAgain(t *testing.T) {
	t.Parallel()

	p, _, machines, c := idleRig(t)
	machines.stopErr = errors.New("the node runs a Dispatch")

	p.Sweep(t.Context())
	c.pass(MachineIdle)
	p.Sweep(t.Context())
	assert.Empty(t, machines.stopped)

	machines.stopErr = nil
	c.pass(SweepEvery)
	p.Sweep(t.Context())
	assert.Equal(t, []string{"n-phone"}, machines.stopped)
}

// Only a machine the control plane made, on, whose node said its phone is
// not running, is stopped.
func TestOnlyAnIdleMachineOfAProviderIsStopped(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*orchestration.Node, *fakeLink){
		"installed by the owner": func(n *orchestration.Node, _ *fakeLink) { n.Machine = nil },
		"not made yet":           func(n *orchestration.Node, _ *fakeLink) { n.Machine.ResourceID = "" },
		"stopped already":        func(n *orchestration.Node, _ *fakeLink) { n.Machine.Power = orchestration.MachineStopped },
		"node offline":           func(_ *orchestration.Node, l *fakeLink) { l.online = map[string]bool{} },
		"phone state not said":   func(_ *orchestration.Node, l *fakeLink) { l.devices = map[string]nodeproto.DeviceState{} },
		"phone being installed":  func(_ *orchestration.Node, l *fakeLink) { l.said("n-phone", nodeproto.DeviceInstalling) },
		"phone starting":         func(_ *orchestration.Node, l *fakeLink) { l.said("n-phone", nodeproto.DeviceStarting) },
	} {
		link := newLink()
		link.connect("n-phone")
		link.said("n-phone", nodeproto.DeviceStopped)

		n := phoneNode("")
		n.Machine.ResourceID = "i-0123"
		change(&n, link)

		machines := &fakeMachines{link: link}
		p, c := viewPhone(link, machines, newEnd(), n)

		p.Sweep(t.Context())
		c.pass(2 * MachineIdle)
		p.Sweep(t.Context())
		assert.Empty(t, machines.stopped, name)
	}
}
