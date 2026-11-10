package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Machines created at a provider (issue #69, step 3). This file keeps the
// record: the providers, and where each of their machines is between "asked
// for" and "gone". Calling the provider is internal/controlplane/machines,
// which follows Omnara's internal/machinepool: the node and its token are
// written before the provider is called, and a machine is forgotten only
// once the provider says it is gone.

// MachineProviderKind names a provider's API.
type MachineProviderKind string

// The kinds.
const (
	MachineProviderAWS MachineProviderKind = "aws"
)

// MachinesPerProviderDefault is a provider's limit when the owner names none.
const MachinesPerProviderDefault = 3

// MachineProvider is an account at a provider that machines are created in.
// It holds no secret: KeyRef names the vault secret that does.
type MachineProvider struct {
	ID          string
	Name        string
	Kind        MachineProviderKind
	KeyRef      string
	Region      string
	Size        string
	MaxMachines int
	// Options are what only this kind takes, such as a subnet.
	Options   map[string]string
	CreatedAt time.Time
}

// NewMachineProvider is what the owner gives to add a provider.
type NewMachineProvider struct {
	Name        string
	Kind        MachineProviderKind
	KeyRef      string
	Region      string
	Size        string
	MaxMachines int
	Options     map[string]string
}

// MachineState is where a provider's machine is.
type MachineState string

// Machine states.
const (
	// MachineProvisioning: asked for, not heard from yet.
	MachineProvisioning MachineState = "provisioning"
	// MachineReady: its node said hello.
	MachineReady MachineState = "ready"
	// MachineProvisionFailed: the provider refused, or the node never said hello.
	MachineProvisionFailed MachineState = "provision_failed"
	// MachineDeleting: being destroyed at the provider.
	MachineDeleting MachineState = "deleting"
	// MachineDeleteFailed: the provider did not destroy it; it is tried again.
	MachineDeleteFailed MachineState = "delete_failed"
)

// Machine is what a node created at a provider records besides the node.
type Machine struct {
	ProviderID string
	// ResourceID is the provider's id for the machine; empty until it answered.
	ResourceID string
	Region     string
	Size       string
	State      MachineState
	// Error says why the last step failed.
	Error string
	// Attempts counts failed deletions; RetryAt is when the next is due.
	Attempts int
	RetryAt  *time.Time
}

// NewMachine is what the owner gives to create a machine.
type NewMachine struct {
	Name          string
	ProviderID    string
	Region        string
	Size          string
	MaxDispatches int
}

// AddMachineProvider records a provider.
func (s *Service) AddMachineProvider(ctx context.Context, n NewMachineProvider) (MachineProvider, error) {
	n.Name, n.Region, n.Size = strings.TrimSpace(n.Name), strings.TrimSpace(n.Region), strings.TrimSpace(n.Size)

	switch {
	case n.Name == "" || n.KeyRef == "" || n.Region == "" || n.Size == "":
		return MachineProvider{}, fmt.Errorf("%w: a machine provider needs a name, a key, a region and a size", ErrInvalid)
	case n.Kind != MachineProviderAWS:
		return MachineProvider{}, fmt.Errorf("%w: unknown machine provider kind %q", ErrInvalid, n.Kind)
	case n.MaxMachines < 0:
		return MachineProvider{}, fmt.Errorf("%w: a limit of zero (default) or more machines", ErrInvalid)
	}

	if n.MaxMachines == 0 {
		n.MaxMachines = MachinesPerProviderDefault
	}

	options, err := json.Marshal(nonNilMap(n.Options))
	if err != nil {
		return MachineProvider{}, fmt.Errorf("encoding options: %w", err)
	}

	id := s.newID("mpr")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertMachineProvider(ctx, store.InsertMachineProviderParams{
			ID: id, Name: n.Name, Kind: string(n.Kind), KeyRef: n.KeyRef, Region: n.Region, Size: n.Size,
			MaxMachines: int64(n.MaxMachines), Options: string(options), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting machine provider %q: %w", ErrConflict, n.Name, err)
		}

		return nil
	})
	if err != nil {
		return MachineProvider{}, fmt.Errorf("adding a machine provider: %w", err)
	}

	return s.GetMachineProvider(ctx, id)
}

// GetMachineProvider returns one provider by id.
func (s *Service) GetMachineProvider(ctx context.Context, id string) (MachineProvider, error) {
	row, err := s.read().GetMachineProvider(ctx, id)
	if err != nil {
		return MachineProvider{}, notFound(err, "machine provider", id)
	}

	return machineProviderOut(row)
}

// GetMachineProviderByName returns one provider by its name.
func (s *Service) GetMachineProviderByName(ctx context.Context, name string) (MachineProvider, error) {
	row, err := s.read().GetMachineProviderByName(ctx, name)
	if err != nil {
		return MachineProvider{}, notFound(err, "machine provider", name)
	}

	return machineProviderOut(row)
}

// ListMachineProviders returns every provider by name.
func (s *Service) ListMachineProviders(ctx context.Context) ([]MachineProvider, error) {
	rows, err := s.read().ListMachineProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing machine providers: %w", err)
	}

	out := make([]MachineProvider, 0, len(rows))

	for _, r := range rows {
		p, err := machineProviderOut(r)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	return out, nil
}

// RemoveMachineProvider forgets a provider. It is refused while a machine of
// it has not been removed, so no machine is left that nothing can destroy.
func (s *Service) RemoveMachineProvider(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		live, err := q.CountLiveNodesOfProvider(ctx, nullString(id))
		if err != nil {
			return fmt.Errorf("counting the machines of provider %s: %w", id, err)
		}

		if live > 0 {
			return fmt.Errorf("%w: machine provider %s still has %d machines; remove them first", ErrConflict, id, live)
		}

		n, err := q.DeleteMachineProvider(ctx, id)

		return oneRow(n, err, "machine provider", id)
	})
}

// MachineProvidersUsingKeyRef names the machine providers whose key is ref,
// so the vault can refuse to delete a secret one still names.
func (s *Service) MachineProvidersUsingKeyRef(ctx context.Context, ref string) ([]string, error) {
	names, err := s.read().ListMachineProviderNamesByKeyRef(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("listing the machine providers that use %q: %w", ref, err)
	}

	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "machine provider "+n)
	}

	return out, nil
}

// BeginMachine records a node whose machine is about to be asked for and
// returns it with its first token and the token's id. It is refused when the
// provider is at its limit. Nothing has been created yet: the caller asks
// the provider next and reports with MachineProvisioned or
// MachineProvisionFailed.
func (s *Service) BeginMachine(ctx context.Context, n NewMachine) (Node, string, string, error) {
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" || n.MaxDispatches < 0 || n.Region == "" || n.Size == "" {
		return Node{}, "", "", fmt.Errorf("%w: a machine needs a name, a region, a size and a limit of zero or more",
			ErrInvalid)
	}

	if n.MaxDispatches == 0 {
		n.MaxDispatches = NodeDispatchesDefault
	}

	token, err := newNodeToken()
	if err != nil {
		return Node{}, "", "", err
	}

	id, tokenID := s.newID("nod"), s.newID("ntk")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		p, err := q.GetMachineProvider(ctx, n.ProviderID)
		if err != nil {
			return notFound(err, "machine provider", n.ProviderID)
		}

		live, err := q.CountLiveNodesOfProvider(ctx, nullString(p.ID))
		if err != nil {
			return fmt.Errorf("counting the machines of provider %s: %w", p.ID, err)
		}

		if live >= p.MaxMachines {
			return fmt.Errorf("%w: machine provider %s is at its limit of %d machines", ErrUnavailable, p.Name,
				p.MaxMachines)
		}

		if err := q.InsertMachineNode(ctx, store.InsertMachineNodeParams{
			ID: id, Name: n.Name, MaxDispatches: int64(n.MaxDispatches), ProviderID: nullString(p.ID),
			Region: nullString(n.Region), Size: nullString(n.Size), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting node %q: %w", ErrConflict, n.Name, err)
		}

		return q.InsertNodeToken(ctx, store.InsertNodeTokenParams{
			ID: tokenID, NodeID: id, Name: firstNodeTokenName, TokenHash: tokenHash(token), Now: now,
		})
	})
	if err != nil {
		return Node{}, "", "", fmt.Errorf("adding a machine: %w", err)
	}

	node, err := s.GetNode(ctx, id)

	return node, token, tokenID, err
}

// MachineProvisioned records the provider's id for a node's machine.
func (s *Service) MachineProvisioned(ctx context.Context, nodeID, resourceID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetMachineResource(ctx, store.SetMachineResourceParams{
			Now: now, ProviderResourceID: nullString(resourceID), ID: nodeID,
		})

		return oneRow(n, err, "machine", nodeID)
	})
}

// MachineProvisionFailed marks a machine that is still waited for as failed,
// with the reason. One that has said hello meanwhile stays ready.
func (s *Service) MachineProvisionFailed(ctx context.Context, nodeID, reason string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.FailMachineProvisioning(ctx, store.FailMachineProvisioningParams{
			Now: now, Error: nullString(reason), ID: nodeID,
		})
		if err != nil {
			return fmt.Errorf("marking machine %s as failed: %w", nodeID, err)
		}

		return nil
	})
}

// BeginMachineRemoval starts removing a provider's node: it takes no more
// work and its tokens stop working, and its machine is to be destroyed. It
// is refused while the node runs Dispatches. Starting again is not an error.
func (s *Service) BeginMachineRemoval(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetNode(ctx, nodeID)
		if err != nil {
			return notFound(err, "node", nodeID)
		}

		if NodeStatus(row.Status) == NodeDeleted || !row.ProviderID.Valid {
			return fmt.Errorf("%w: machine %s", ErrNotFound, nodeID)
		}

		active, err := q.CountActiveDispatchesOfNode(ctx, store.CountActiveDispatchesOfNodeParams{
			NodeID: nodeID, Statuses: activeDispatchStatuses(),
		})
		if err != nil {
			return fmt.Errorf("counting the dispatches of node %s: %w", nodeID, err)
		}

		if active > 0 {
			return fmt.Errorf("%w: node %s still runs %d Dispatches; drain it and let them end", ErrConflict, nodeID,
				active)
		}

		if _, err := q.StartMachineDeletion(ctx, store.StartMachineDeletionParams{Now: now, ID: nodeID}); err != nil {
			return fmt.Errorf("marking machine %s as deleting: %w", nodeID, err)
		}

		if err := q.RevokeNodeTokens(ctx, store.RevokeNodeTokensParams{Now: validInt(now), NodeID: nodeID}); err != nil {
			return fmt.Errorf("revoking the tokens of node %s: %w", nodeID, err)
		}

		return nil
	})
}

// MachineRemoved records that the provider no longer has the machine: the
// node is removed.
func (s *Service) MachineRemoved(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.FinishMachineDeletion(ctx, store.FinishMachineDeletionParams{Now: now, ID: nodeID})

		return oneRow(n, err, "machine being deleted", nodeID)
	})
}

// MachineRemovalFailed records why the provider did not destroy a machine
// and when to try again. The node stays listed until it is destroyed.
func (s *Service) MachineRemovalFailed(ctx context.Context, nodeID, reason string, retryAt time.Time) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.FailMachineDeletion(ctx, store.FailMachineDeletionParams{
			Now: now, Error: nullString(reason), RetryAt: validInt(retryAt.UnixMilli()), ID: nodeID,
		})

		return oneRow(n, err, "machine being deleted", nodeID)
	})
}

// ListUnsettledMachines returns the provider nodes the control plane still
// owes a step: those waited for and those being destroyed.
func (s *Service) ListUnsettledMachines(ctx context.Context) ([]Node, error) {
	rows, err := s.read().ListUnsettledMachines(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing unsettled machines: %w", err)
	}

	return s.nodesOf(ctx, rows)
}

// ListMachinesOfProvider returns a provider's nodes that have not been removed.
func (s *Service) ListMachinesOfProvider(ctx context.Context, providerID string) ([]Node, error) {
	rows, err := s.read().ListLiveMachinesOfProvider(ctx, nullString(providerID))
	if err != nil {
		return nil, fmt.Errorf("listing the machines of provider %s: %w", providerID, err)
	}

	return s.nodesOf(ctx, rows)
}

func (s *Service) nodesOf(ctx context.Context, rows []store.Node) ([]Node, error) {
	p, err := loadPlacement(ctx, s.read(), s.clock.Now().UnixMilli())
	if err != nil {
		return nil, err
	}

	out := make([]Node, 0, len(rows))

	for _, r := range rows {
		if n, ok := p.nodes[r.ID]; ok {
			out = append(out, p.nodeOut(n))
		}
	}

	return out, nil
}

func machineOut(r store.Node) *Machine {
	if !r.ProviderID.Valid {
		return nil
	}

	return &Machine{
		ProviderID: r.ProviderID.String, ResourceID: r.ProviderResourceID.String, Region: r.Region.String,
		Size: r.Size.String, State: MachineState(r.MachineState.String), Error: r.MachineError.String,
		Attempts: int(r.MachineAttempts), RetryAt: optionalTime(r.MachineRetryAt),
	}
}

func machineProviderOut(r store.MachineProvider) (MachineProvider, error) {
	options := map[string]string{}
	if err := json.Unmarshal([]byte(r.Options), &options); err != nil {
		return MachineProvider{}, fmt.Errorf("decoding the options of machine provider %s: %w", r.ID, err)
	}

	return MachineProvider{
		ID: r.ID, Name: r.Name, Kind: MachineProviderKind(r.Kind), KeyRef: r.KeyRef, Region: r.Region, Size: r.Size,
		MaxMachines: int(r.MaxMachines), Options: options, CreatedAt: millis(r.CreatedAt),
	}, nil
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}
