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
	now   int64
	nodes map[string]*nodeLoad
	order []*nodeLoad
	// groups counts the unsettled Dispatches of each Bot's Runs, "" the
	// owner's Runs without a Bot (issue #660; runGroup).
	groups   map[string]int
	accounts []store.Account
	cooling  map[[2]string]int64
	// reasons says why each cooling account cools.
	reasons map[[2]string]string
}

type nodeLoad struct {
	row      store.Node
	runtimes []RuntimeInfo
	// hosts are the git hosts the node reached; said is false for a node that
	// never reported, which is taken to reach every host.
	hosts []string
	said  bool
	// tools is what the node has of the tools it was told to have; nil when it has not said.
	tools  []NodeTool
	active int
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

	groups, err := q.CountActiveDispatchesPerBot(ctx, activeDispatchStatuses())
	if err != nil {
		return nil, fmt.Errorf("counting active dispatches per bot: %w", err)
	}

	cooldowns, err := q.ListActiveCooldowns(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("listing cooldowns: %w", err)
	}

	p := &placement{
		now: now, nodes: map[string]*nodeLoad{}, groups: map[string]int{}, accounts: accounts,
		cooling: map[[2]string]int64{}, reasons: map[[2]string]string{},
	}

	for _, row := range nodes {
		n := &nodeLoad{row: row}
		if err := json.Unmarshal([]byte(row.Runtimes), &n.runtimes); err != nil {
			return nil, fmt.Errorf("decoding the runtimes of node %s: %w", row.ID, err)
		}

		if row.GitHosts.Valid {
			n.said = true
			if err := json.Unmarshal([]byte(row.GitHosts.String), &n.hosts); err != nil {
				return nil, fmt.Errorf("decoding the git hosts of node %s: %w", row.ID, err)
			}
		}

		if row.Tools.Valid {
			if err := json.Unmarshal([]byte(row.Tools.String), &n.tools); err != nil {
				return nil, fmt.Errorf("decoding the tools of node %s: %w", row.ID, err)
			}

			n.tools = nonNil(n.tools)
		}

		p.nodes[row.ID] = n
		p.order = append(p.order, n)
	}

	for _, g := range groups {
		p.groups[g.Bot] = int(g.N)
	}

	for _, l := range loads {
		if n, ok := p.nodes[l.NodeID]; ok {
			n.active = int(l.N)
		}
	}

	for _, c := range cooldowns {
		p.cooling[[2]string{c.Runtime, c.AccountRef}] = c.Until
		p.reasons[[2]string{c.Runtime, c.AccountRef}] = c.Reason
	}

	return p, nil
}

func (p *placement) connected(n *nodeLoad) bool {
	return n.row.LastSeenAt.Valid && p.now-n.row.LastSeenAt.Int64 < NodeOfflineAfter.Milliseconds()
}

func (n *nodeLoad) offers(runtime string) bool {
	return slices.ContainsFunc(n.runtimes, func(r RuntimeInfo) bool { return r.Runtime == runtime })
}

// reaches reports whether work on a git host can run on the node. A Run with
// no repository has no host and runs anywhere.
func (n *nodeLoad) reaches(host string) bool {
	return host == "" || !n.said || slices.Contains(n.hosts, host)
}

// gitHosts is what the node said it reaches: nil when it has not said.
func (n *nodeLoad) gitHosts() []string {
	if !n.said {
		return nil
	}

	return nonNil(n.hosts)
}

// checkNode says why a node cannot take a Dispatch of the runtime, on the
// git host, now.
func (p *placement) checkNode(id, runtime string, w where) error {
	host := w.host

	n, ok := p.nodes[id]
	if !ok {
		return fmt.Errorf("%w: node %s", ErrNotFound, id)
	}

	if err := p.serves(n, runtime); err != nil {
		return err
	}

	switch {
	case !n.reaches(host):
		return fmt.Errorf("%w: node %s does not reach %s", ErrUnavailable, id, host)
	case n.lacks(w.tools) != "":
		return fmt.Errorf("%w: node %s has no %s", ErrUnavailable, id, n.lacks(w.tools))
	case n.active >= int(n.row.MaxDispatches):
		return fmt.Errorf("%w: node %s runs %d of %d", ErrUnavailable, id, n.active, n.row.MaxDispatches)
	}

	return nil
}

// checkAccount says why an account cannot take a Dispatch of the runtime now.
func (p *placement) checkAccount(runtime, id string) error {
	if id == GatewayAccountRef {
		return nil // the gateway serves the model; its accounts are its own to choose and to cool down
	}

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
	case gatewayOnly(runtime):
		return fmt.Errorf("%w: a %s worker runs through the model gateway, not under account %s", ErrInvalid,
			runtime, id)
	case !runsOnNode(a):
		return fmt.Errorf("%w: account %s was logged in on the control plane; no node has it", ErrInvalid, id)
	}

	if until, ok := p.cooling[[2]string{runtime, id}]; ok {
		return fmt.Errorf("%w: %s/%s until %s", ErrCoolingDown, runtime, id, millis(until))
	}

	return nil
}

// serves says why a node runs no worker of the runtime, whatever its load
// and whichever git host the work is on.
func (p *placement) serves(n *nodeLoad, runtime string) error {
	id := n.row.ID

	switch {
	case NodeStatus(n.row.Status) != NodeActive:
		return fmt.Errorf("%w: node %s is drained", ErrUnavailable, id)
	case !p.connected(n):
		return fmt.Errorf("%w: node %s is not connected", ErrUnavailable, id)
	case n.row.UnavailableAt.Valid:
		return fmt.Errorf("%w: node %s let its dispatches expire", ErrUnavailable, id)
	case !n.offers(runtime):
		return fmt.Errorf("%w: node %s has no %s", ErrUnavailable, id, runtime)
	}

	return nil
}

// availability says whether a worker of the runtime can start now, as the
// scheduler would find: a node that serves the runtime and has a worktree
// root, then an account that is not cooling down, or none for a model the
// gateway serves. A node at its limit still counts: the Task waits its turn.
// A runtime that runs only through the gateway cannot start without it.
func (p *placement) availability(runtime string, gateway bool, accounts string) Availability {
	if !gateway && gatewayOnly(runtime) {
		return Availability{Reason: ReasonNeedsGateway}
	}

	if !slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return p.serves(n, runtime) == nil && n.row.WorktreeRoot.String != ""
	}) {
		return Availability{Reason: ReasonNoNode}
	}

	if gateway && !p.gatewayHasAccount(accounts) {
		return Availability{Reason: ReasonNoAccount}
	}

	if gateway {
		return Availability{Available: true}
	}

	id, cooling := p.pickAccount(runtime)

	switch {
	case id != "":
		return Availability{Available: true}
	case !cooling:
		return Availability{Reason: ReasonNoAccount}
	}

	var first int64

	for k, until := range p.cooling {
		if k[0] == runtime && p.checkAccountKnown(runtime, k[1]) && (first == 0 || until < first) {
			first = until
		}
	}

	at := millis(first)

	return Availability{Reason: ReasonAccountsCooling, At: &at}
}

// checkAccountKnown reports whether an id is an active account of the
// runtime that a node holds: the ones pickAccount looks at.
func (p *placement) checkAccountKnown(runtime, id string) bool {
	return slices.ContainsFunc(p.accounts, func(a store.Account) bool {
		return a.ID == id && a.Runtime == runtime && AccountStatus(a.Status) == AccountActive && runsOnNode(a)
	})
}

// atLimit reports whether a node could take a Dispatch of the runtime, on the
// git host, but for its own limit (issue #660).
func (p *placement) atLimit(id, runtime string, w where) bool {
	n, ok := p.nodes[id]

	return ok && n.active >= int(n.row.MaxDispatches) && p.serves(n, runtime) == nil && n.reaches(w.host) &&
		n.lacks(w.tools) == ""
}

// anyAtLimit reports whether a node with a worktree root could take a new
// Conversation of the runtime but for its own limit.
func (p *placement) anyAtLimit(runtime string, w where) bool {
	return slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return n.row.WorktreeRoot.String != "" && p.atLimit(n.row.ID, runtime, w)
	})
}

// nodeReady reports whether a node can take a Dispatch of the runtime, on the git host, now.
func (p *placement) nodeReady(id, runtime string, w where) bool {
	return p.checkNode(id, runtime, w) == nil
}

// where is what a Run's work needs of the node it goes to: a route to its
// git host, and the tools its Connections are called with.
type where struct {
	host  string
	tools []string
}

// lacks names the first of the tools the node has not installed, "" when it
// has them all. A node that has not said what it has, has none.
func (n *nodeLoad) lacks(tools []string) string {
	for _, tool := range tools {
		if !slices.ContainsFunc(n.tools, func(t NodeTool) bool { return t.Name == tool && t.Version != "" }) {
			return tool
		}
	}

	return ""
}

// anyHas reports whether a connected, active node has every one of the tools.
func (p *placement) anyHas(tools []string) (missing string) {
	for _, n := range p.order {
		if NodeStatus(n.row.Status) != NodeActive || !p.connected(n) {
			continue
		}

		if n.lacks(tools) == "" {
			return ""
		}

		missing = n.lacks(tools)
	}

	if missing == "" && len(tools) > 0 {
		missing = tools[0]
	}

	return missing
}

// anyReaches reports whether a connected, active node reaches the git host.
func (p *placement) anyReaches(host string) bool {
	return slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return NodeStatus(n.row.Status) == NodeActive && p.connected(n) && n.reaches(host)
	})
}

// accountUsable reports whether an account can take a Dispatch now, and
// whether it only waits for a cooldown to end.
func (p *placement) accountUsable(runtime, id string) (usable, cooling bool) {
	err := p.checkAccount(runtime, id)

	return err == nil, errors.Is(err, ErrCoolingDown)
}

// pickNode is the node with the most free capacity that can take a new
// Conversation of the runtime on the git host (issue #731): it filters, then
// scores, as Kubernetes filters with NodeResourcesFit before scoring
// (helper/resource_fit.go:136-145) and Nomad ranks only Ready() nodes
// (structs.go:2168-2170). A node low on memory is skipped (lowMemory). The
// highest score wins (better); equal scores go to the node with more free slots
// by count, as Jenkins Least Load takes the most idle executors
// (LeastLoadBalancer.java:443-470), then to the smaller id, as Orca's relay
// does (assignment-store.ts:8164), to keep placement reproducible.
func (p *placement) pickNode(runtime string, host where) *nodeLoad {
	var best *nodeLoad

	for _, n := range p.order {
		if !p.nodeReady(n.row.ID, runtime, host) || !n.row.WorktreeRoot.Valid || n.row.WorktreeRoot.String == "" ||
			p.lowMemory(n) {
			continue
		}

		if best == nil || p.better(n, best) {
			best = n
		}
	}

	return best
}

// better reports whether node a goes before node b: a higher score, then more
// free slots by count, then the smaller id.
//
// A node's score is its free capacity, 0 to 1: the mean of its free shares of
// slots, CPU and memory. From Kubernetes LeastAllocated, each resource's
// (capacity - requested) / capacity averaged with weights, CPU and memory
// weight 1 (least_allocated.go:30-62, apis/config/v1/defaults.go:34-35), and
// Nomad's spread over free CPU and memory shares (structs/funcs.go:234-250);
// the slots share as Orca's relay loadRatio = load / capacity
// (assignment-store.ts:8156-8165); measured CPU as Trimaran's
// TargetLoadPacking reads it (targetloadpacking.go:131-144). The slot shares
// are compared exactly, as fractions, so two nodes whose measured shares are
// equal (none fresh, for one) are ordered by their slots alone.
func (p *placement) better(a, b *nodeLoad) bool {
	freeA, maxA := a.free(), a.row.MaxDispatches
	freeB, maxB := b.free(), b.row.MaxDispatches

	// The sum of the three shares orders as their mean does.
	slots := float64(freeA*maxB-freeB*maxA) / float64(maxA*maxB)

	if d := slots + p.measured(a) - p.measured(b); d != 0 {
		return d > 0
	}

	if freeA != freeB {
		return freeA > freeB
	}

	return a.row.ID < b.row.ID
}

// free is how many more Dispatches the node may run under its own limit.
func (n *nodeLoad) free() int64 { return max(n.row.MaxDispatches-int64(n.active), 0) }

// measured is the sum of a node's free CPU and memory shares, each 0 to 1. A
// node without fresh stats, or that never said its memory, counts 0 for what
// it lacks: it ranks below any node that reports room and is still used when
// it is the only one, as Trimaran scores a node without metrics at the
// minimum (targetloadpacking.go:107-117).
func (p *placement) measured(n *nodeLoad) float64 {
	if !p.freshStats(n) {
		return 0
	}

	var sum float64

	if n.row.CpuPercent.Valid {
		sum += clamp01(1 - n.row.CpuPercent.Float64/100)
	}

	if total := n.row.MemoryBytes.Int64; n.row.MemoryBytes.Valid && total > 0 && n.row.MemoryUsedBytes.Valid {
		sum += clamp01(1 - float64(n.row.MemoryUsedBytes.Int64)/float64(total))
	}

	return sum
}

// freshStats reports whether the node's last measurement is younger than
// NodeOfflineAfter, three heartbeats.
func (p *placement) freshStats(n *nodeLoad) bool {
	return n.row.StatsAt.Valid && p.now-n.row.StatsAt.Int64 < NodeOfflineAfter.Milliseconds()
}

// lowMemory reports whether a node's fresh stats say it has less than
// NodeMemoryFloor available: it takes no new Conversation, as a kubelet under
// MemoryPressure is tainted NoSchedule for new pods only
// (kubelet/eviction/defaults_linux.go:22-27, node_lifecycle_controller.go:95-96).
// A Conversation that continues keeps its node. A node without fresh stats or
// without its memory size is not refused.
func (p *placement) lowMemory(n *nodeLoad) bool {
	return p.freshStats(n) && n.row.MemoryBytes.Valid && n.row.MemoryBytes.Int64 > 0 &&
		n.row.MemoryUsedBytes.Valid && n.row.MemoryBytes.Int64-n.row.MemoryUsedBytes.Int64 < NodeMemoryFloor
}

// anyLowMemory reports whether a node with a worktree root could take a new
// Conversation of the runtime but for its memory.
func (p *placement) anyLowMemory(runtime string, w where) bool {
	return slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return n.row.WorktreeRoot.String != "" && p.nodeReady(n.row.ID, runtime, w) && p.lowMemory(n)
	})
}

func clamp01(x float64) float64 { return min(max(x, 0), 1) }

// TokenRuntime is the runtime whose account can be a token the owner pastes:
// Claude Code's `claude setup-token`, long-lived and with no refresh to share
// (issue #150). The vault keeps it and the model gateway serves the account;
// it never leaves the control plane (issue #381).
const TokenRuntime = "claude"

// runsOnNode reports whether a worker on a node can run under an account:
// one whose CLI was logged in on that node. An account whose secret the
// control plane keeps is the gateway's to serve.
func runsOnNode(a store.Account) bool { return !a.SecretRef.Valid }

// gatewayHasAccount reports whether the control plane keeps an active account of the runtime for the gateway to
// serve a subscription under; a model of the gateway's own table (no runtime) needs none here. Whether that account
// cools down is the gateway's to find.
func (p *placement) gatewayHasAccount(runtime string) bool {
	if runtime == "" {
		return true
	}

	return slices.ContainsFunc(p.accounts, func(a store.Account) bool {
		return a.Runtime == runtime && AccountStatus(a.Status) == AccountActive && !runsOnNode(a)
	})
}

// pickAccount is the first active account of the runtime a node holds, by
// position, that is not cooling down. cooling reports whether one exists but cools.
func (p *placement) pickAccount(runtime string) (id string, cooling bool) {
	if gatewayOnly(runtime) {
		return "", false
	}

	for _, a := range p.accounts {
		if a.Runtime != runtime || AccountStatus(a.Status) != AccountActive || !runsOnNode(a) {
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

// take counts a new Dispatch on a node and in its Bot's group.
func (p *placement) take(nodeID, group string) {
	p.groups[group]++

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

		// A list that does not decode is dropped: the rest of the measurement still stands.
		if n.row.Gpus.Valid {
			_ = json.Unmarshal([]byte(n.row.Gpus.String), &stats.GPUs)
		}
	}

	return Node{
		Stats: stats, Machine: machineOut(n.row),
		ID: n.row.ID, Name: n.row.Name, Status: NodeStatus(n.row.Status), MaxDispatches: int(n.row.MaxDispatches),
		WorktreeRoot: n.row.WorktreeRoot.String, Runtimes: nonNil(n.runtimes), GitHosts: n.gitHosts(),
		Tools: n.tools,
		System: SystemInfo{
			DaemonVersion: n.row.DaemonVersion.String, OS: n.row.Os.String, Arch: n.row.Arch.String,
			CPUs: int(n.row.Cpus.Int64), MemoryBytes: n.row.MemoryBytes.Int64, DiskBytes: n.row.DiskBytes.Int64,
		},
		LastSeenAt:    optionalTime(n.row.LastSeenAt),
		UnavailableAt: optionalTime(n.row.UnavailableAt), Connected: p.connected(n), Active: n.active,
		Terminal: n.row.Terminal == 1, Device: n.row.Device, KVM: n.row.Kvm == 1,
		CreatedAt: millis(n.row.CreatedAt), UpdatedAt: millis(n.row.UpdatedAt),
	}
}

func (p *placement) accountOut(a store.Account) Account {
	out := Account{
		ID: a.ID, Runtime: a.Runtime, Label: a.Label, Status: AccountStatus(a.Status), Position: int(a.Position),
		ExternalID: a.ExternalID.String, Email: a.Email.String, Plan: a.Plan.String, Project: a.Project.String,
		SecretRef: a.SecretRef.String,
		Refresh: AccountRefresh{
			Failures: int(a.RefreshFailures), RetryAt: optionalTime(a.RefreshRetryAt), Error: a.RefreshError.String,
		},
	}

	if until, ok := p.cooling[[2]string{a.Runtime, a.ID}]; ok {
		t := millis(until)
		out.CoolingUntil = &t
		out.CoolingReason = p.reasons[[2]string{a.Runtime, a.ID}]
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
