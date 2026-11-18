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
	// owner's Runs without a Bot (issue #660; runGroup): the used side of
	// each group's used / guarantee, which orders the groups (issue #731).
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
	// requested is the sum of the requests of its unsettled Dispatches (issue #731).
	requested request
}

// request is what a Dispatch asks of a node: CPU in thousandths of a core,
// and memory in bytes (issue #731), as a Kubernetes container's requests
// (helper/resource_fit.go:159-178) and a Nomad task's resources.
type request struct {
	cpuMillis, memoryBytes int64
}

func (r request) plus(o request) request {
	return request{cpuMillis: r.cpuMillis + o.cpuMillis, memoryBytes: r.memoryBytes + o.memoryBytes}
}

// requestOf is a profile version's request, each part the default for
// building work when the profile asks for nothing.
func requestOf(v store.ProfileVersion) request {
	r := request{cpuMillis: DefaultCPURequestMillis, memoryBytes: DefaultMemoryRequest}

	if v.CpuRequestMillis.Valid {
		r.cpuMillis = v.CpuRequestMillis.Int64
	}

	if v.MemoryRequestBytes.Valid {
		r.memoryBytes = v.MemoryRequestBytes.Int64
	}

	return r
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
			n.requested = request{cpuMillis: l.CpuMillis, memoryBytes: l.MemoryBytes}
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

// takesWork reports whether a node can be given new work: the owner has not
// drained it, it is connected, and a provider's machine is on. The owner's
// drain and the machine's power are kept apart, so that a stop and a start
// leave the drain as the owner set it (issue #844), as Nomad's Node.Ready
// (nomad/structs/structs.go) asks for a ready status and scheduling
// eligibility, two fields, and the kube-scheduler filters a cordoned node
// apart from one that is not ready.
func (p *placement) takesWork(n *nodeLoad) bool {
	return NodeStatus(n.row.Status) == NodeActive && p.connected(n) && !n.row.MachinePower.Valid
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
	if err := p.checkNodeFit(id, runtime, w); err != nil {
		return err
	}

	if p.lowDisk(id) {
		return fmt.Errorf("%w: node %s: %s", ErrUnavailable, id, ReasonNodesLowDisk)
	}

	return nil
}

func (p *placement) checkNodeFit(id, runtime string, w where) error {
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
	case !n.fits(w.req):
		return fmt.Errorf("%w: node %s has no room: %s", ErrUnavailable, id, n.room(w.req))
	}

	return nil
}

// capped reports whether the owner set the node's optional cap.
func (n *nodeLoad) capped() bool { return n.row.MaxDispatches.Valid }

// sized reports whether the node said its capacity, its CPUs and its memory,
// in its hello. A node that did not has no capacity: it takes work only under
// its cap (issue #731; inferred: no reference schedules on a node of unknown
// capacity).
func (n *nodeLoad) sized() bool {
	return n.row.Cpus.Valid && n.row.Cpus.Int64 > 0 && n.row.MemoryBytes.Valid && n.row.MemoryBytes.Int64 > 0
}

// allocatable is what the node's Dispatches may ask for in all: its capacity
// less what the owner reserved, and its memory less NodeMemoryFloor too,
// never below 0. From the kubelet's allocatable = capacity - system-reserved -
// kube-reserved - hard eviction (cm/node_container_manager_linux.go:292-313,
// nodestatus/setters.go:310-320) and Nomad's client reserved
// (nomad/structs/funcs.go:192-198).
func (n *nodeLoad) allocatable() request {
	if !n.sized() {
		return request{}
	}

	return request{
		cpuMillis:   less(n.row.Cpus.Int64*1000, n.row.ReservedCpuMillis),
		memoryBytes: less(less(n.row.MemoryBytes.Int64, n.row.ReservedMemoryBytes), NodeMemoryFloor),
	}
}

// less is have - take, never below 0. Both are 0 or more, so it cannot
// overflow, however large a reserve the owner set (PR #759).
func less(have, take int64) int64 {
	if take >= have {
		return 0
	}

	return have - take
}

// fits reports whether one more Dispatch of the request fits the node now:
// under its cap when one is set, and with the requests of its unsettled
// Dispatches plus this one within its allocatable CPU and memory both. From
// Kubernetes FitsRequest, "Too many pods" then "Insufficient cpu/memory"
// (helper/resource_fit.go:136-178), and Nomad's AllocsFit, node_max_allocs
// then resources (nomad/structs/funcs.go:145-149, 192-198); both fit on
// declared requests, never on measured use.
func (n *nodeLoad) fits(r request) bool {
	if n.capped() && int64(n.active) >= n.row.MaxDispatches.Int64 {
		return false
	}

	if !n.sized() {
		return n.capped()
	}

	alloc, used := n.allocatable(), n.requested.plus(r)

	return used.cpuMillis <= alloc.cpuMillis && used.memoryBytes <= alloc.memoryBytes
}

// fitsEmpty reports whether the request would fit the node with nothing on
// it: when it does not, waiting frees nothing (Kubernetes marks such a pod
// Unresolvable, helper/resource_fit.go:166, 176).
func (n *nodeLoad) fitsEmpty(r request) bool {
	if !n.sized() {
		return n.capped()
	}

	alloc := n.allocatable()

	return r.cpuMillis <= alloc.cpuMillis && r.memoryBytes <= alloc.memoryBytes
}

// room says why a request does not fit the node, for an error.
func (n *nodeLoad) room(r request) string {
	switch {
	case n.capped() && int64(n.active) >= n.row.MaxDispatches.Int64:
		return fmt.Sprintf("it runs %d of its cap of %d", n.active, n.row.MaxDispatches.Int64)
	case !n.sized():
		return "it has not said its CPUs and memory, and has no cap"
	}

	alloc, used := n.allocatable(), n.requested.plus(r)

	return fmt.Sprintf("%d of %d CPU millicores and %d of %d memory bytes would be requested", used.cpuMillis,
		alloc.cpuMillis, used.memoryBytes, alloc.memoryBytes)
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
	case n.row.MachinePower.Valid:
		return fmt.Errorf("%w: the machine of node %s is %s", ErrUnavailable, id, n.row.MachinePower.String)
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

// could reports whether a node runs workers of the runtime and suits the
// Run's work, whatever room it has.
func (p *placement) could(n *nodeLoad, runtime string, w where) bool {
	return p.serves(n, runtime) == nil && n.reaches(w.host) && n.lacks(w.tools) == ""
}

// atLimit reports whether a node could take a Dispatch of the runtime, on the
// git host, but is full now: the request would fit it empty, but not with what
// it runs, or it runs as many as its cap (issues #660, #731).
func (p *placement) atLimit(id, runtime string, w where) bool {
	n, ok := p.nodes[id]

	return ok && p.could(n, runtime, w) && !n.fits(w.req) && n.fitsEmpty(w.req)
}

// tooSmall reports whether a node could take a Dispatch of the runtime, on
// the git host, but for a request larger than its allocatable: waiting frees
// nothing there (issue #731).
func (p *placement) tooSmall(id, runtime string, w where) bool {
	n, ok := p.nodes[id]

	return ok && p.could(n, runtime, w) && !n.fitsEmpty(w.req)
}

// anyAtLimit reports whether a node with a worktree root could take a new
// Conversation of the runtime but is full now.
func (p *placement) anyAtLimit(runtime string, w where) bool {
	return slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return n.row.WorktreeRoot.String != "" && p.atLimit(n.row.ID, runtime, w)
	})
}

// anyTooSmall reports whether a node with a worktree root could take a new
// Conversation of the runtime but for its size.
func (p *placement) anyTooSmall(runtime string, w where) bool {
	return slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return n.row.WorktreeRoot.String != "" && p.tooSmall(n.row.ID, runtime, w)
	})
}

// nodeReady reports whether a node can take a Dispatch of the runtime, on the git host, now.
func (p *placement) nodeReady(id, runtime string, w where) bool {
	return p.checkNode(id, runtime, w) == nil
}

// where is what a Run's work needs of the node it goes to: a route to its
// git host, the tools its Connections are called with, and the request of
// the Dispatch (issue #731).
type where struct {
	host  string
	tools []string
	req   request
}

// asking is where with the request of the resolved profile.
func (w where) asking(r resolved) where {
	w.req = r.req

	return w
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

// anyHas reports whether a node that takes work has every one of the tools.
func (p *placement) anyHas(tools []string) (missing string) {
	for _, n := range p.order {
		if !p.takesWork(n) {
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

// anyReaches reports whether a node that takes work reaches the git host.
func (p *placement) anyReaches(host string) bool {
	return slices.ContainsFunc(p.order, func(n *nodeLoad) bool {
		return p.takesWork(n) && n.reaches(host)
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
// (structs.go:2168-2170). The filter is checkNode, the fit by requests among
// it; a node low on memory is skipped (lowMemory). The highest-ranked node
// wins (better).
func (p *placement) pickNode(runtime string, host where, recentExpiries map[string]bool) *nodeLoad {
	var best *nodeLoad

	for _, n := range p.order {
		if !p.nodeReady(n.row.ID, runtime, host) || !n.row.WorktreeRoot.Valid || n.row.WorktreeRoot.String == "" ||
			p.lowMemory(n) {
			continue
		}

		if best == nil || p.better(n, best, host.req, recentExpiries) {
			best = n
		}
	}

	return best
}

// better reports whether node a goes before node b for a Dispatch of the
// request: a node without a recent expiry for the Task (Nomad's rescheduling
// penalty), then a higher score, more memory left by requests, and the smaller id.
//
// A node's score is its free capacity, 0 to 1: the mean of its free share by
// requests and its measured free CPU and memory shares. From Kubernetes
// LeastAllocated, each resource's (allocatable - requested) / allocatable
// averaged with weights, CPU and memory weight 1 (least_allocated.go:30-62,
// resource_allocation.go:200-210, apis/config/v1/defaults.go:34-35), and
// Nomad's spread over free CPU and memory shares (structs/funcs.go:234-250);
// measured CPU as Trimaran's TargetLoadPacking reads it
// (targetloadpacking.go:131-144). The tie-break ranks by absolute free room,
// as Jenkins Least Load ranks by idle executors (LeastLoadBalancer.java:443-470),
// then by id, as Orca's relay does (assignment-store.ts:8164), to keep
// placement reproducible.
func (p *placement) better(a, b *nodeLoad, r request, recentExpiries map[string]bool) bool {
	if recentExpiries[a.row.ID] != recentExpiries[b.row.ID] {
		return !recentExpiries[a.row.ID]
	}

	// The sum of the three shares orders as their mean does. Each sum is
	// computed the same way, so equal nodes tie exactly.
	if sa, sb := a.free(r)+p.measured(a), b.free(r)+p.measured(b); sa != sb {
		return sa > sb
	}

	if la, lb := a.memoryLeft(), b.memoryLeft(); la != lb {
		return la > lb
	}

	return a.row.ID < b.row.ID
}

// free is the node's free share by requests, 0 to 1, with one more Dispatch
// of the request on it: the mean of 1 - requested/allocatable over CPU and
// memory. A node that did not say its size counts 0, as Trimaran scores a
// node without metrics at the minimum (targetloadpacking.go:107-117).
func (n *nodeLoad) free(r request) float64 {
	alloc := n.allocatable()
	if alloc.cpuMillis <= 0 || alloc.memoryBytes <= 0 {
		return 0
	}

	used := n.requested.plus(r)

	return (clamp01(1-float64(used.cpuMillis)/float64(alloc.cpuMillis)) +
		clamp01(1-float64(used.memoryBytes)/float64(alloc.memoryBytes))) / 2
}

// memoryLeft is the allocatable memory its unsettled Dispatches do not ask for, in bytes.
func (n *nodeLoad) memoryLeft() int64 {
	return max(n.allocatable().memoryBytes-n.requested.memoryBytes, 0)
}

// measured is the sum of a node's free CPU and memory shares, each 0 to 1. A
// node without fresh stats, or that never said its memory, counts 0 for both,
// as Trimaran scores a node without metrics at the minimum
// (targetloadpacking.go:107-117); its free share by requests still counts, so
// it may outrank a node with fresh stats whose requests leave less room.
func (p *placement) measured(n *nodeLoad) float64 {
	total := n.row.MemoryBytes.Int64
	if !p.freshStats(n) || !n.row.MemoryBytes.Valid || total <= 0 || !n.row.MemoryUsedBytes.Valid {
		return 0
	}

	sum := clamp01(1 - float64(n.row.MemoryUsedBytes.Int64)/float64(total))

	if n.row.CpuPercent.Valid {
		sum += clamp01(1 - n.row.CpuPercent.Float64/100)
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

// MuseRuntime is the runtime of a Muse account (issue #782): one of the
// owner's muse.ai accounts, whose credential is its cookie jar in the vault.
// No worker runs under it; the Muse pool (internal/controlplane/muse) asks
// muse.ai with it.
const MuseRuntime = "muse"

// OpenCodeGoRuntime is the runtime of an OpenCode Go key (issue #854): a Console API key the owner pastes, kept in
// the vault. No worker runs under it; the model gateway serves the provider of kind ProviderOpenCodeGo with it.
const OpenCodeGoRuntime = "opencode_go"

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

// take counts a new Dispatch of the request on a node and in its Bot's group.
func (p *placement) take(nodeID, group string, r request) {
	p.groups[group]++

	if n, ok := p.nodes[nodeID]; ok {
		n.active++
		n.requested = n.requested.plus(r)
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

	alloc := n.allocatable()

	return Node{
		Stats: stats, Machine: machineOut(n.row), Disk: diskStatus(n.row, p.now),
		ID: n.row.ID, Name: n.row.Name, Status: NodeStatus(n.row.Status), MaxDispatches: int(n.row.MaxDispatches.Int64),
		Reserved:     Resources{CPUMillis: n.row.ReservedCpuMillis, MemoryBytes: n.row.ReservedMemoryBytes},
		Allocatable:  Resources{CPUMillis: alloc.cpuMillis, MemoryBytes: alloc.memoryBytes},
		Requested:    Resources{CPUMillis: n.requested.cpuMillis, MemoryBytes: n.requested.memoryBytes},
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
