package phone

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// An agent's command on the phone's node whose machine was stopped for idleness starts the machine and goes on once
// its node said hello, as a call does (issue #1083); a command on another node starts nothing.
func TestACommandOnThePhonesNodeWakesItsStoppedMachine(t *testing.T) {
	t.Parallel()

	link := newLink()
	machines := &fakeMachines{link: link, connect: true}
	p := newPhone(link, machines, phoneNode(orchestration.MachineStopped))

	done, woken, err := p.Use(t.Context(), "n-build")
	require.NoError(t, err)
	assert.False(t, woken)
	assert.Empty(t, machines.started, "another node's command starts nothing")
	done()

	done, woken, err = p.Use(t.Context(), "n-phone")
	require.NoError(t, err)
	assert.True(t, woken)
	assert.Equal(t, []string{"n-phone"}, machines.started)
	assert.True(t, link.Online("n-phone"))
	done()
}

// A stopped machine that cannot be woken in time is answered with why it was stopped and how it starts, never as a
// node not connected (issue #1083).
func TestACommandThatCannotWakeThePhoneSaysWhy(t *testing.T) {
	t.Parallel()

	link := newLink()
	machines := &fakeMachines{link: link}
	p := newPhone(link, machines, phoneNode(orchestration.MachineStopped))

	_, _, err := p.Use(t.Context(), "n-phone")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Contains(t, err.Error(), "the phone was idle")
	assert.Contains(t, err.Error(), "the phone is waking")
	assert.Contains(t, err.Error(), "device_screen")
	assert.NotContains(t, err.Error(), "not connected")
	assert.Equal(t, []string{"n-phone"}, machines.started)
	p.mu.Lock()
	assert.Zero(t, p.commands, "a command that did not run is not counted")
	p.mu.Unlock()
}

// A command running on the phone's node keeps its machine on, however long it runs, and the idle time starts when it
// ends (issue #1083).
func TestACommandInFlightKeepsTheMachineOn(t *testing.T) {
	t.Parallel()

	p, _, machines, c := idleRig(t)

	p.Sweep(t.Context())
	c.pass(MachineIdle - time.Minute)

	done, _, err := p.Use(t.Context(), "n-phone")
	require.NoError(t, err)

	c.pass(time.Hour)
	p.Sweep(t.Context())
	assert.Empty(t, machines.stopped, "a running command keeps the machine on")

	done()
	p.Sweep(t.Context())
	c.pass(MachineIdle - time.Second)
	p.Sweep(t.Context())
	assert.Empty(t, machines.stopped, "the idle time starts when the command ends")

	c.pass(time.Second)
	p.Sweep(t.Context())
	assert.Equal(t, []string{"n-phone"}, machines.stopped)
}

// A command that comes while a sweep is stopping the machine is refused with why, and does not run on a machine about
// to go off; the stop goes on (review of PR #1085, round 1).
func TestACommandComingWhileTheMachineStopsIsRefused(t *testing.T) {
	t.Parallel()

	p, _, machines, c := idleRig(t)
	machines.stopHold, machines.stopEntered = make(chan struct{}), make(chan struct{}, 1)

	p.Sweep(t.Context())
	c.pass(MachineIdle)

	swept := make(chan struct{})

	go func() {
		defer close(swept)

		p.Sweep(t.Context())
	}()

	<-machines.stopEntered

	_, _, err := p.Use(t.Context(), "n-phone")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Contains(t, err.Error(), "being stopped")
	assert.Contains(t, err.Error(), "device_screen")
	assert.NotContains(t, err.Error(), "not connected")

	p.mu.Lock()
	assert.Zero(t, p.commands, "the command is not admitted")
	p.mu.Unlock()

	close(machines.stopHold)
	<-swept
	assert.Equal(t, []string{"n-phone"}, machines.stopped)
}

// A machine whose stop is on its record is started for a command even while its node's socket is still open, as it
// is for a while after the provider was asked to stop it: its power, not its socket, says it is off.
func TestAStoppedMachineWhoseSocketIsStillOpenIsWoken(t *testing.T) {
	t.Parallel()

	link := newLink()
	link.connect("n-phone")

	machines := &fakeMachines{link: link, connect: true}
	p := newPhone(link, machines, phoneNode(orchestration.MachineStopped))

	done, woken, err := p.Use(t.Context(), "n-phone")
	require.NoError(t, err)
	assert.True(t, woken)
	assert.Equal(t, []string{"n-phone"}, machines.started)
	done()
}
