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

	done, woken, err = p.Use(t.Context(), "n-phone")
	require.NoError(t, err)
	assert.False(t, woken, "a connected node is not woken again")
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
	assert.Contains(t, err.Error(), "stopped its machine because the phone was idle")
	assert.Contains(t, err.Error(), "the phone is waking")
	assert.Contains(t, err.Error(), "device_screen")
	assert.NotContains(t, err.Error(), "not connected")
	assert.Equal(t, []string{"n-phone"}, machines.started)
	assert.Zero(t, p.commands.Load(), "a command that did not run is not counted")
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
