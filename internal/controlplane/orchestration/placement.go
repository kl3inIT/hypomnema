package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// placement is a snapshot of capacity read inside one transaction: the
// nodes with their load, the accounts and their cooldowns. The scheduler
// updates it as it places, so one pass never exceeds a limit.
type placement struct {
	now      int64
	active   int
	nodes    map[string]*nodeLoad
	order    []*nodeLoad
	accounts []store.Account
	cooling  map[[2]string]int64
}

type nodeLoad struct {
	row      store.Node
	runtimes []RuntimeInfo
	active   int
}

func loadPlacement(ctx context.Context, q *store.Queries, now int64) (*placement, error) {
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	loads, err := q.CountActiveDispatchesPerNode(ctx, activeDispatchStatuses())
	if err != nil {
		return nil, fmt.Errorf("counting active dispatches: %w", err)
	}

	accounts, err := q.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}

	cooldowns, err := q.ListActiveCooldowns(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("listing cooldowns: %w", err)
	}

	p := &placement{
		now: now, nodes: map[string]*nodeLoad{}, accounts: accounts, cooling: map[[2]string]int64{},
	}

	for _, row := range nodes {
		n := &nodeLoad{row: row}
		if err := json.Unmarshal([]byte(row.Runtimes), &n.runtimes); err != nil {
			return nil, fmt.Errorf("decoding the runtimes of node %s: %w", row.ID, err)
		}

		p.nodes[row.ID] = n
		p.order = append(p.order, n)
	}

	for _, l := range loads {
		p.active += int(l.N)
		if n, ok := p.nodes[l.NodeID]; ok {
			n.active = int(l.N)
		}
	}

	for _, c := range cooldowns {
		p.cooling[[2]string{c.Runtime, c.AccountRef}] = c.Until
	}

	return p, nil
}

func (p *placement) connected(n *nodeLoad) bool {
	return n.row.LastSeenAt.Valid && p.now-n.row.LastSeenAt.Int64 < NodeOfflineAfter.Milliseconds()
}

func (n *nodeLoad) offers(runtime string) bool {
	return slices.ContainsFunc(n.runtimes, func(r RuntimeInfo) bool { return r.Runtime == runtime })
}

// checkNode says why a node cannot take a Dispatch of the runtime now.
func (p *placement) checkNode(id, runtime string) error {
	n, ok := p.nodes[id]

	switch {
	case !ok:
		return fmt.Errorf("%w: node %s", ErrNotFound, id)
	case NodeStatus(n.row.Status) != NodeActive:
		return fmt.Errorf("%w: node %s is drained", ErrUnavailable, id)
	case !p.connected(n):
		return fmt.Errorf("%w: node %s is not connected", ErrUnavailable, id)
	case n.row.UnavailableAt.Valid:
		return fmt.Errorf("%w: node %s let its dispatches expire", ErrUnavailable, id)
	case !n.offers(runtime):
		return fmt.Errorf("%w: node %s has no %s", ErrUnavailable, id, runtime)
	case n.active >= int(n.row.MaxDispatches):
		return fmt.Errorf("%w: node %s runs %d of %d", ErrUnavailable, id, n.active, n.row.MaxDispatches)
	}

	return nil
}

// checkAccount says why an account cannot take a Dispatch of the runtime now.
func (p *placement) checkAccount(runtime, id string) error {
	i := slices.IndexFunc(p.accounts, func(a store.Account) bool { return a.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: account %s", ErrNotFound, id)
	}

	a := p.accounts[i]

	switch {
	case a.Runtime != runtime:
		return fmt.Errorf("%w: account %s is for %s, not %s", ErrInvalid, id, a.Runtime, runtime)
	case AccountStatus(a.Status) != AccountActive:
		return fmt.Errorf("%w: account %s is disabled", ErrUnavailable, id)
	case a.SecretRef.Valid:
		return fmt.Errorf("%w: account %s was logged in on the control plane; no node has it", ErrInvalid, id)
	}

	if until, ok := p.cooling[[2]string{runtime, id}]; ok {
		return fmt.Errorf("%w: %s/%s until %s", ErrCoolingDown, runtime, id, millis(until))
	}

	return nil
}

// nodeReady reports whether a node can take a Dispatch of the runtime now.
func (p *placement) nodeReady(id, runtime string) bool { return p.checkNode(id, runtime) == nil }

// accountUsable reports whether an account can take a Dispatch now, and
// whether it only waits for a cooldown to end.
func (p *placement) accountUsable(runtime, id string) (usable, cooling bool) {
	err := p.checkAccount(runtime, id)

	return err == nil, errors.Is(err, ErrCoolingDown)
}

// pickNode is the least loaded node that can take a new Conversation of the
// runtime, ties by id (inferred: Coder does no balancing, Orca takes the
// first free terminal).
func (p *placement) pickNode(runtime string) *nodeLoad {
	var best *nodeLoad

	for _, n := range p.order {
		if !p.nodeReady(n.row.ID, runtime) || !n.row.WorktreeRoot.Valid || n.row.WorktreeRoot.String == "" {
			continue
		}

		if best == nil || n.active < best.active || (n.active == best.active && n.row.ID < best.row.ID) {
			best = n
		}
	}

	return best
}

// pickAccount is the first active account of the runtime a node holds, by
// position, that is not cooling down. cooling reports whether one exists but cools.
func (p *placement) pickAccount(runtime string) (id string, cooling bool) {
	for _, a := range p.accounts {
		if a.Runtime != runtime || AccountStatus(a.Status) != AccountActive || a.SecretRef.Valid {
			continue
		}

		if _, ok := p.cooling[[2]string{runtime, a.ID}]; ok {
			cooling = true

			continue
		}

		return a.ID, false
	}

	return "", cooling
}

// take counts a new Dispatch on a node.
func (p *placement) take(nodeID string) {
	p.active++
	if n, ok := p.nodes[nodeID]; ok {
		n.active++
	}
}

func (p *placement) nodeOut(n *nodeLoad) Node {
	var stats *SystemStats
	if n.row.StatsAt.Valid {
		stats = &SystemStats{
			CPUPercent: n.row.CpuPercent.Float64, MemoryUsedBytes: n.row.MemoryUsedBytes.Int64,
			DiskUsedBytes: n.row.DiskUsedBytes.Int64, Load1: n.row.Load1.Float64, At: millis(n.row.StatsAt.Int64),
		}
	}

	return Node{
		Stats: stats, Machine: machineOut(n.row),
		ID: n.row.ID, Name: n.row.Name, Status: NodeStatus(n.row.Status), MaxDispatches: int(n.row.MaxDispatches),
		WorktreeRoot: n.row.WorktreeRoot.String, Runtimes: nonNil(n.runtimes),
		System: SystemInfo{
			DaemonVersion: n.row.DaemonVersion.String, OS: n.row.Os.String, Arch: n.row.Arch.String,
			CPUs: int(n.row.Cpus.Int64), MemoryBytes: n.row.MemoryBytes.Int64, DiskBytes: n.row.DiskBytes.Int64,
		},
		LastSeenAt:    optionalTime(n.row.LastSeenAt),
		UnavailableAt: optionalTime(n.row.UnavailableAt), Connected: p.connected(n), Active: n.active,
		CreatedAt: millis(n.row.CreatedAt), UpdatedAt: millis(n.row.UpdatedAt),
	}
}

func (p *placement) accountOut(a store.Account) Account {
	out := Account{
		ID: a.ID, Runtime: a.Runtime, Label: a.Label, Status: AccountStatus(a.Status), Position: int(a.Position),
		ExternalID: a.ExternalID.String, Email: a.Email.String, Plan: a.Plan.String, SecretRef: a.SecretRef.String,
		Refresh: AccountRefresh{
			Failures: int(a.RefreshFailures), RetryAt: optionalTime(a.RefreshRetryAt), Error: a.RefreshError.String,
		},
	}

	if until, ok := p.cooling[[2]string{a.Runtime, a.ID}]; ok {
		t := millis(until)
		out.CoolingUntil = &t
	}

	return out
}

// activeDispatchStatuses are the statuses that hold a slot: every one that
// has not settled, a Dispatch waiting on a question included (Coder).
func activeDispatchStatuses() []string {
	var out []string

	for _, s := range dispatchOrder {
		if !s.settled() {
			out = append(out, string(s))
		}
	}

	return out
}
