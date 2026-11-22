package phone

import (
	"context"
	"fmt"
)

// An agent's command on the phone's node (node_exec and node_process, issue #1083) is a use of the phone, as a call
// and a view are: it starts the phone's machine that the control plane stopped for idleness, as a call does (wake),
// and the machine is not stopped while the command runs. Coder's agent tools start a stopped workspace that a call
// names before running in it (.tmp/coder codersdk/toolsdk/workspaceagent.go:31-70 findWorkspaceAndAgent), and an
// open session counts as the workspace's activity, which keeps it from stopping (coderd/workspacestats/reporter.go:
// 156-194).

// Use readies the node nodeID for an agent's command when it holds the phone, and counts the command as the phone's
// use until done is called: a machine that is stopped, or being started, is started and its hello waited for, within
// WakeWithin and ctx. woken says the node was not connected and is now. For another node Use does nothing; for the
// phone's node that is offline with its machine on, it wakes nothing and says so with woken false, as for any node
// whose socket is not back yet. Its error is ErrUnavailable's, and says why the machine is stopped and how it is
// started.
func (p *Phone) Use(ctx context.Context, nodeID string) (done func(), woken bool, err error) {
	n, err := p.node(ctx)
	if err != nil || n.ID != nodeID {
		return func() {}, false, nil //nolint:nilerr // A control plane without a phone runs commands as before.
	}

	p.commands.Add(1)
	p.busy()

	done = func() {
		p.commands.Add(-1)
		p.busy()
	}

	if p.cfg.Link.Online(n.ID) || n.Machine == nil || n.Machine.Power == "" {
		return done, false, nil
	}

	if err := p.wake(ctx, n); err != nil {
		done()

		return nil, false, fmt.Errorf("node %q holds the phone, and the control plane stopped its machine because "+
			"the phone was idle (stopped, with no call, view or command, for %s). Starting it for this command: "+
			"%w. Any phone tool's call, such as device_screen, starts it too, as the owner can on the Machines "+
			"page; it does not come back by itself", n.Name, MachineIdle, err)
	}

	return done, true, nil
}
