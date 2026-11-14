// Package phone gives agents the Android phone a node holds (issue #417,
// step 2), as package browser gives them the shared browser: the tools
// (internal/shared/devicetool) and one call of a tool, which the node that
// holds the phone runs (internal/node/device, tools.go). It is given as the
// browser is (#212): every worker has it, with no setting and no Connection
// (owner, 2026-11-15).
//
// The phone has one holder at a time: a call waits until the call before it
// has ended, so no call runs on a screen another left half done, as
// OpenMausBot's phone proxy lets one turn hold the phone
// (server/drivers/phone-proxy.ts) and T3 Code holds a device for one thread.
// A call that finds the phone's machine stopped starts it, through the same
// path as `hypomnema machines start`, and waits a bounded time for its node's
// hello, as the issue's "Machine lifecycle" row says (started by the first
// call when stopped; inference, for cost: no reference stops or starts a
// whole machine for a device).
package phone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// WakeWithin is how long a call waits for a stopped machine's node to say
	// hello after it was started: about a minute on the trial machine.
	WakeWithin = 2 * time.Minute
	// pollEvery is how often the wait looks for the node.
	pollEvery = 2 * time.Second
)

// ErrUnavailable reports a phone that cannot be used now, and why.
var ErrUnavailable = errors.New("the phone is not available")

// Nodes reads the nodes, to find the one that holds the phone.
type Nodes interface {
	ListNodes(ctx context.Context) ([]orchestration.Node, error)
}

// Link reaches a node on its socket (nodeapi.Server).
type Link interface {
	Online(nodeID string) bool
	DeviceCall(ctx context.Context, nodeID, agent, tool string, arguments json.RawMessage,
	) (nodeproto.DeviceAnswer, error)
}

// Machines starts a stopped machine (machines.Manager).
type Machines interface {
	Start(ctx context.Context, nodeID string) (orchestration.Node, error)
}

// Config is what a Phone needs; Machines may be nil, and a stopped machine
// is then not started.
type Config struct {
	Nodes    Nodes
	Link     Link
	Machines Machines
	Clock    platform.Clock
	Logger   *slog.Logger
	// WakeWithin and Poll replace the package's limits, for a test.
	WakeWithin time.Duration
	Poll       time.Duration
}

// Phone is the phone agents share.
type Phone struct {
	cfg Config
	// holder is held by the call that uses the phone.
	holder chan struct{}
}

// New returns a Phone.
func New(cfg Config) *Phone {
	if cfg.Clock == nil {
		cfg.Clock = platform.SystemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.WakeWithin <= 0 {
		cfg.WakeWithin = WakeWithin
	}

	if cfg.Poll <= 0 {
		cfg.Poll = pollEvery
	}

	return &Phone{cfg: cfg, holder: make(chan struct{}, 1)}
}

// Tools lists the phone's tools; ErrUnavailable when no node holds a phone,
// so that a worker of a control plane without one is offered none.
func (p *Phone) Tools(ctx context.Context) ([]devicetool.Tool, error) {
	if _, err := p.node(ctx); err != nil {
		return nil, err
	}

	return devicetool.Tools(), nil
}

// Held says whether a node holds a phone, for an offer to say so.
func (p *Phone) Held(ctx context.Context) bool {
	_, err := p.node(ctx)

	return err == nil
}

// Call runs one phone tool for an agent and returns its result as MCP has
// it: content and isError. It waits for the call before it to end.
func (p *Phone) Call(ctx context.Context, agent, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	if _, err := devicetool.Read(tool, arguments); err != nil {
		return nil, err
	}

	select {
	case p.holder <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	defer func() { <-p.holder }()

	n, err := p.node(ctx)
	if err != nil {
		return nil, err
	}

	if !p.cfg.Link.Online(n.ID) {
		if err := p.wake(ctx, n); err != nil {
			return nil, err
		}
	}

	answer, err := p.cfg.Link.DeviceCall(ctx, n.ID, agent, tool, arguments)
	if errors.Is(err, orchestration.ErrUnavailable) {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(answer)
	if err != nil {
		return nil, fmt.Errorf("encoding the phone's answer: %w", err)
	}

	return raw, nil
}

// node is the node that holds the phone: the first, as one phone is kept
// (several phones are not in issue #417).
func (p *Phone) node(ctx context.Context) (orchestration.Node, error) {
	nodes, err := p.cfg.Nodes.ListNodes(ctx)
	if err != nil {
		return orchestration.Node{}, err
	}

	for _, n := range nodes {
		if n.Device == nodeproto.DeviceAndroid {
			return n, nil
		}
	}

	return orchestration.Node{}, fmt.Errorf("%w: no node of this control plane holds a phone", ErrUnavailable)
}

// wake has the phone's node connected: its machine is started when it is
// stopped, and its hello waited for.
func (p *Phone) wake(ctx context.Context, n orchestration.Node) error {
	if n.Machine == nil {
		return fmt.Errorf("%w: the phone's node %s is offline", ErrUnavailable, n.Name)
	}

	switch n.Machine.Power {
	case orchestration.MachineStopping:
		return fmt.Errorf("%w: the phone's machine is being stopped; try again in a minute", ErrUnavailable)
	case orchestration.MachineStopped:
		if p.cfg.Machines == nil {
			return fmt.Errorf("%w: the phone's machine is stopped", ErrUnavailable)
		}

		// The machine is started for the call; it stays on when the wait below gives up.
		if _, err := p.cfg.Machines.Start(ctx, n.ID); err != nil {
			return fmt.Errorf("%w: the phone's machine could not be started: %w", ErrUnavailable, err)
		}

		p.cfg.Logger.InfoContext(ctx, "the phone's machine was started for an agent's call",
			slog.String("event", "phone.machine_started"), slog.String("node", n.ID))
	case orchestration.MachineStarting:
		// Started already, by the owner or a call before: its hello is waited for.
	default:
		return fmt.Errorf("%w: the phone's node %s is offline though its machine is on", ErrUnavailable, n.Name)
	}

	deadline := time.NewTimer(p.cfg.WakeWithin)
	defer deadline.Stop()

	tick := time.NewTicker(p.cfg.Poll)
	defer tick.Stop()

	for !p.cfg.Link.Online(n.ID) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: the phone is waking: its machine is starting, which takes about a minute. "+
				"Try again in a minute", ErrUnavailable)
		case <-tick.C:
		}
	}

	return nil
}
