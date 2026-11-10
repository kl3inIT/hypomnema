package machines

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// HelloTimeout is how long a new machine has to say hello before it is
	// marked failed. It stays at the provider until the owner removes it.
	HelloTimeout = 10 * time.Minute
	// adoptAfter is how old a machine with no provider id must be before a
	// pass looks for it: younger, its create may still be running.
	adoptAfter = 2 * time.Minute
	// SettleEvery is how often the loop looks at unsettled machines.
	SettleEvery = 30 * time.Second
	// callTimeout bounds one call to a provider.
	callTimeout = time.Minute

	deleteRetryFirst = 30 * time.Second
	deleteRetryMax   = time.Hour
)

// Registry is what the manager needs of the orchestration service.
type Registry interface {
	GetNode(ctx context.Context, id string) (orchestration.Node, error)
	RemoveNode(ctx context.Context, nodeID string) error
	GetMachineProvider(ctx context.Context, id string) (orchestration.MachineProvider, error)
	GetMachineProviderByName(ctx context.Context, name string) (orchestration.MachineProvider, error)
	BeginMachine(ctx context.Context, n orchestration.NewMachine) (orchestration.Node, string, string, error)
	MachineProvisioned(ctx context.Context, nodeID, resourceID string) error
	MachineProvisionFailed(ctx context.Context, nodeID, reason string) error
	BeginMachineRemoval(ctx context.Context, nodeID string) error
	MachineRemoved(ctx context.Context, nodeID string) error
	MachineRemovalFailed(ctx context.Context, nodeID, reason string, retryAt time.Time) error
	ListUnsettledMachines(ctx context.Context) ([]orchestration.Node, error)
	ListMachinesOfProvider(ctx context.Context, providerID string) ([]orchestration.Node, error)
}

// Connect returns the Provider for a provider record, opening its key.
type Connect func(ctx context.Context, p orchestration.MachineProvider) (Provider, error)

// Manager creates and removes machines and settles the ones left between.
type Manager struct {
	reg       Registry
	connect   Connect
	publicURL string
	clock     platform.Clock
	logger    *slog.Logger
}

// New returns a Manager. publicURL is the control plane's URL as a new
// machine reaches it; without one no machine can be created.
func New(reg Registry, connect Connect, publicURL string, clock platform.Clock, logger *slog.Logger) *Manager {
	return &Manager{reg: reg, connect: connect, publicURL: publicURL, clock: clock, logger: logger}
}

// NewMachine is what the owner gives to create a machine.
type NewMachine struct {
	Name string
	// Provider is the provider's name.
	Provider string
	// Region and Size are the provider's defaults when empty.
	Region        string
	Size          string
	MaxDispatches int
}

// Create asks the provider for a machine and returns its node, which is
// `provisioning` until the machine says hello. When the provider refuses,
// the node is returned as `provision_failed` together with the error.
func (m *Manager) Create(ctx context.Context, n NewMachine) (orchestration.Node, error) {
	rec, err := m.reg.GetMachineProviderByName(ctx, n.Provider)
	if err != nil {
		return orchestration.Node{}, err
	}

	region, size := orDefault(n.Region, rec.Region), orDefault(n.Size, rec.Size)

	// Everything that can be refused without a machine is checked before the node exists.
	if _, err := FirstBoot(m.publicURL, "check"); err != nil {
		return orchestration.Node{}, fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	}

	p, err := m.connect(ctx, rec)
	if err != nil {
		return orchestration.Node{}, err
	}

	if err := m.call(ctx, func(ctx context.Context) error { return p.Validate(ctx, region, size) }); err != nil {
		return orchestration.Node{}, fmt.Errorf("%w: machine provider %s: %w", orchestration.ErrInvalid, rec.Name, err)
	}

	node, token, tokenID, err := m.reg.BeginMachine(ctx, orchestration.NewMachine{
		Name: n.Name, ProviderID: rec.ID, Region: region, Size: size, MaxDispatches: n.MaxDispatches,
	})
	if err != nil {
		return orchestration.Node{}, err
	}

	script, err := FirstBoot(m.publicURL, token)
	if err != nil {
		return m.failed(ctx, node, err)
	}

	var resourceID string

	err = m.call(ctx, func(ctx context.Context) error {
		id, err := p.Create(ctx, Spec{
			NodeID: node.ID, Name: node.Name, Region: region, Size: size, UserData: script, RequestID: tokenID,
		})
		resourceID = id

		return err
	})
	if err != nil {
		return m.failed(ctx, node, fmt.Errorf("machine provider %s: %w", rec.Name, err))
	}

	// The machine exists from here on: a failure to record its id is settled by the loop, which finds it by its node.
	if err := m.reg.MachineProvisioned(context.WithoutCancel(ctx), node.ID, resourceID); err != nil {
		return orchestration.Node{}, err
	}

	return m.reg.GetNode(ctx, node.ID)
}

func (m *Manager) failed(ctx context.Context, node orchestration.Node, cause error) (orchestration.Node, error) {
	ctx = context.WithoutCancel(ctx)

	if err := m.reg.MachineProvisionFailed(ctx, node.ID, cause.Error()); err != nil {
		return orchestration.Node{}, errors.Join(cause, err)
	}

	got, err := m.reg.GetNode(ctx, node.ID)
	if err != nil {
		return orchestration.Node{}, errors.Join(cause, err)
	}

	return got, cause
}

// Remove takes a node out of the registry. A provider's machine is destroyed
// first; when the provider does not destroy it now, the node stays listed as
// `delete_failed`, the loop tries again, and the error is returned.
func (m *Manager) Remove(ctx context.Context, nodeID string) error {
	node, err := m.reg.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}

	if node.Machine == nil {
		return m.reg.RemoveNode(ctx, nodeID)
	}

	if err := m.reg.BeginMachineRemoval(ctx, nodeID); err != nil {
		return err
	}

	// Once the node is marked, the destroy is not abandoned with the request.
	return m.destroy(context.WithoutCancel(ctx), node)
}

// destroy asks the provider to destroy a node's machine and records the outcome.
func (m *Manager) destroy(ctx context.Context, node orchestration.Node) error {
	err := m.withProvider(ctx, node, func(ctx context.Context, p Provider) error {
		return p.Delete(ctx, node.Machine.Region, node.ID)
	})
	if err == nil {
		return m.reg.MachineRemoved(ctx, node.ID)
	}

	retryAt := m.clock.Now().Add(deleteRetryWait(node.Machine.Attempts))
	if recErr := m.reg.MachineRemovalFailed(ctx, node.ID, err.Error(), retryAt); recErr != nil {
		return errors.Join(err, recErr)
	}

	return fmt.Errorf("the machine of node %s is not destroyed yet and is tried again at %s: %w", node.ID,
		retryAt.UTC().Format(time.RFC3339), err)
}

func (m *Manager) withProvider(
	ctx context.Context, node orchestration.Node, fn func(context.Context, Provider) error,
) error {
	rec, err := m.reg.GetMachineProvider(ctx, node.Machine.ProviderID)
	if err != nil {
		return err
	}

	p, err := m.connect(ctx, rec)
	if err != nil {
		return err
	}

	return m.call(ctx, func(ctx context.Context) error { return fn(ctx, p) })
}

func (m *Manager) call(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	return fn(ctx)
}

// Settle takes every unsettled machine one step: a machine waited for too
// long is marked failed, one whose create was cut short is looked up at the
// provider, and a deletion that is due is tried again. An error with one
// machine does not stop the others; they are joined.
func (m *Manager) Settle(ctx context.Context) error {
	nodes, err := m.reg.ListUnsettledMachines(ctx)
	if err != nil {
		return err
	}

	var errs []error

	for _, n := range nodes {
		if err := m.settle(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", n.ID, err))
		}
	}

	return errors.Join(errs...)
}

func (m *Manager) settle(ctx context.Context, n orchestration.Node) error {
	now := m.clock.Now()

	switch n.Machine.State {
	case orchestration.MachineProvisioning:
		age := now.Sub(n.CreatedAt)

		if n.Machine.ResourceID == "" && age >= adoptAfter {
			return m.adopt(ctx, n)
		}

		if age >= HelloTimeout {
			return m.reg.MachineProvisionFailed(ctx, n.ID,
				fmt.Sprintf("the machine did not say hello within %s", HelloTimeout))
		}

		return nil
	case orchestration.MachineDeleting, orchestration.MachineDeleteFailed:
		if n.Machine.RetryAt != nil && now.Before(*n.Machine.RetryAt) {
			return nil
		}

		// A deletion with no retry time was cut short before its first try ended.
		if n.Machine.RetryAt == nil && now.Sub(n.UpdatedAt) < adoptAfter {
			return nil
		}

		return m.destroy(ctx, n)
	case orchestration.MachineReady, orchestration.MachineProvisionFailed:
		return nil
	default:
		return fmt.Errorf("unknown machine state %q", n.Machine.State)
	}
}

// adopt settles a machine whose create ended without an answer being
// recorded: the provider is asked whether it has the node's machine.
func (m *Manager) adopt(ctx context.Context, n orchestration.Node) error {
	var (
		found Resource
		ok    bool
	)

	err := m.withProvider(ctx, n, func(ctx context.Context, p Provider) error {
		var err error
		found, ok, err = p.Find(ctx, n.Machine.Region, n.ID)

		return err
	})
	if err != nil {
		return err
	}

	if !ok {
		return m.reg.MachineProvisionFailed(ctx, n.ID, "the provider has no machine for this node")
	}

	return m.reg.MachineProvisioned(ctx, n.ID, found.ID)
}

// Loop settles machines until ctx is canceled. A failing pass is logged and
// the next one tries again: a provider being down must not stop the server.
func (m *Manager) Loop(ctx context.Context) {
	tick := time.NewTicker(SettleEvery)
	defer tick.Stop()

	for {
		if err := m.Settle(ctx); err != nil && ctx.Err() == nil {
			m.logger.WarnContext(ctx, "settling machines",
				slog.String("event", "machines.settle_failed"), slog.String("error", err.Error()))
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Check asks the provider for the machines this provider record created and
// returns those no live node stands for: machines that cost money and that
// nothing here will destroy. It also proves the key works.
func (m *Manager) Check(ctx context.Context, providerName string) ([]Resource, error) {
	rec, err := m.reg.GetMachineProviderByName(ctx, providerName)
	if err != nil {
		return nil, err
	}

	nodes, err := m.reg.ListMachinesOfProvider(ctx, rec.ID)
	if err != nil {
		return nil, err
	}

	p, err := m.connect(ctx, rec)
	if err != nil {
		return nil, err
	}

	regions, live := []string{rec.Region}, map[string]bool{}

	for _, n := range nodes {
		live[n.ID] = true

		if !slices.Contains(regions, n.Machine.Region) {
			regions = append(regions, n.Machine.Region)
		}
	}

	var orphans []Resource

	for _, region := range regions {
		var found []Resource

		err := m.call(ctx, func(ctx context.Context) error {
			var err error
			found, err = p.List(ctx, region)

			return err
		})
		if err != nil {
			return nil, fmt.Errorf("machine provider %s, region %s: %w", rec.Name, region, err)
		}

		for _, r := range found {
			if !live[r.NodeID] {
				orphans = append(orphans, r)
			}
		}
	}

	return orphans, nil
}

func deleteRetryWait(attempts int) time.Duration {
	wait := deleteRetryFirst
	for range attempts {
		wait *= 2
		if wait >= deleteRetryMax {
			return deleteRetryMax
		}
	}

	return wait
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}

	return v
}
