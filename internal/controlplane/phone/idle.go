package phone

import (
	"context"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The phone's machine is stopped when it has been idle (issue #417, step 4):
// the node stops the emulator 15 minutes after its last call or view
// (internal/node/device), and the control plane stops the machine
// MachineIdle after that, through the same path as `hypomnema machines stop`.
// A call or a view that finds it stopped starts it again (phone.go, wake).
// This is inference, for cost, as the issue's "Machine lifecycle" row says: no
// reference stops a whole machine when its device is idle. Its shape is the
// browser's Sweep (internal/controlplane/browser, engine.go), which closes
// Chrome once it has been unused for a while.

const (
	// MachineIdle is how long the phone stays stopped, with no call and no
	// view, before its machine is stopped: $0.39 an hour on, about $3 a month
	// stopped.
	MachineIdle = 30 * time.Minute
	// SweepEvery is how often the machine's idleness is looked at; a stop that
	// failed is asked again at the next look.
	SweepEvery = time.Minute
)

// busy notes that the phone is in use now: a call or a view began or ended,
// or a sweep found the machine not idle. Its idle time starts at the next
// sweep that finds it idle.
func (p *Phone) busy() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.idleSince = time.Time{}
}

// Sweep stops the phone's machine when the phone has been stopped, with no
// call and no view, for MachineIdle. Only a machine the control plane made
// and that is on is stopped; any other is left alone.
func (p *Phone) Sweep(ctx context.Context) {
	if p.cfg.Machines == nil {
		return
	}

	n, err := p.node(ctx)
	if err != nil || !p.idle(n) {
		p.busy()

		return
	}

	now := p.cfg.Clock.Now()

	p.mu.Lock()
	if p.idleSince.IsZero() {
		p.idleSince = now
	}

	since := p.idleSince
	p.mu.Unlock()

	if now.Sub(since) < MachineIdle {
		return
	}

	// A stop the registry refuses (the node runs a Dispatch) or the provider fails is logged, and asked again at
	// the next sweep: the idle time is not started again.
	if _, err := p.cfg.Machines.Stop(ctx, n.ID); err != nil {
		p.cfg.Logger.WarnContext(ctx, "the phone's idle machine could not be stopped",
			slog.String("event", "phone.machine_stop_failed"), slog.String("node", n.ID),
			slog.String("error", err.Error()))

		return
	}

	p.cfg.Logger.InfoContext(ctx, "the phone's idle machine was stopped",
		slog.String("event", "phone.machine_stopped"), slog.String("node", n.ID))
	p.busy()
}

// idle says whether the phone's machine may be stopped now: a machine the
// control plane made that is on, whose node is connected and said its phone
// is not running, with no agent's call and no owner's view.
func (p *Phone) idle(n orchestration.Node) bool {
	if n.Machine == nil || n.Machine.ResourceID == "" || n.Machine.State != orchestration.MachineReady ||
		n.Machine.Power != "" || !p.cfg.Link.Online(n.ID) {
		return false
	}

	// A call holds the phone while it runs, and a view is counted from its opening to its close.
	if len(p.holder) > 0 || p.hold.Viewing() {
		return false
	}

	st, ok := p.cfg.Link.DeviceState(n.ID)
	if !ok {
		return false
	}

	switch st.State {
	case nodeproto.DeviceStopped, nodeproto.DeviceAbsent, nodeproto.DeviceFailed:
		return true
	default:
		return false
	}
}
