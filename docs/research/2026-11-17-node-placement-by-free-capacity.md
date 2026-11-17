# Placing work on the node with the most free capacity

Studied on 2026-11-17 for issue #731. Question: how a scheduler scores the nodes that can take a piece of work, so
that a new Conversation goes to the node with the most free capacity, not the one with the fewest Dispatches; how it
uses the nodes' measured load, how old that measurement may be, when it refuses a node, and how it breaks ties.

## What Hypomnema does today

Read on `main` at `00943bf`.

- `pickNode` takes, among the nodes that can take the Conversation, the one with the fewest active Dispatches by
  absolute count, ties by id (`internal/controlplane/orchestration/placement.go:346-365`). Its comment marks the rule
  as an inference.
- A node can take one when it is active, connected (heard from within `NodeOfflineAfter`, 30 s,
  `limits.go:86-88`, `placement.go:115-117`), not unavailable, offers the runtime (`serves`, `placement.go:198-212`),
  reaches the git host, has the tools, runs fewer than its `max_dispatches` (`checkNode`, `placement.go:140-160`) and
  has a worktree root (`placement.go:353`).
- `max_dispatches` is a column of `nodes`, default 2 (`limits.go:81-82`, `nodes.go:212-231`), set by the owner from 1
  to 64 (`nodes.go:295-302`).
- A node measures itself at every heartbeat, every 10 s (`nodeproto.go:86`): CPU in use since the heartbeat before,
  0-100 (gopsutil `cpu.Percent` with interval 0), memory in use, the worktree volume's use and the one-minute load
  average (`internal/node/system.go:37-68`). Memory in use is gopsutil's `Total - Available`, `Available` being Linux's
  `MemAvailable` (gopsutil v4.26.10 `mem/mem_linux.go:318-323`). The machine's total memory and CPU count come once,
  with the hello (`system.go:20-32`).
- The control plane stores the latest measurement, replacing the one before, with `stats_at` set to the time it was
  received (`nodeapi.go:663-667`, `nodes.go:549-575`); nothing about it is read when placing.
- When no node can take the Task the scheduler names why (`scheduler.go:400-415`); `nodes_full` waits and is not
  escalated (`dispatch_types.go:320-323`, `scheduler.go:476-479`), the others are escalated after
  `UnplaceableAfter`.

The zm case of 2026-11-17 (issue #731): `elitex` (limit 1, about one core, 3.8 GiB, 99.8% CPU) and `stc-hy-app` (limit
4, 31.4 GiB, 0.1% CPU), both with no Dispatch. Both count 0, so the tie went to the smaller id. Inferred: the ids were
not read on zm. The issue's text also speaks of "0 of 1" against "1 of 4"; with that count the current rule picks
`elitex` without a tie.

## Revisions read

Cloned into `.tmp/` with `--depth 1` on 2026-11-17 (Kubernetes, Nomad and scheduler-plugins sparse).

| Reference | Revision | What was read |
|---|---|---|
| Kubernetes | `52ff44527393` | `pkg/scheduler/framework/plugins/noderesources`, `helper/resource_fit.go`, `framework/sorted_nodes.go`, `schedule_one.go`, `apis/config/v1`, `pkg/kubelet/eviction/defaults_linux.go`, `pkg/controller/nodelifecycle` |
| Nomad | `359056d76692` | `nomad/structs/funcs.go`, `scheduler/feasible/{rank,select,stack,feasible}.go`, `scheduler/util.go` |
| Kubernetes scheduler-plugins (Trimaran) | `a15fe31becaa` | `pkg/trimaran` (TargetLoadPacking, LoadVariationRiskBalancing, collector, handler) |
| Jenkins Least Load plugin (`jenkinsci/leastload-plugin`) | `ec2060b4719f` | `LeastLoadBalancer.java` |
| Buildkite agent | `c6fd8acc9297` | `clicommand/agent_start.go` (priority, spawn priority) |
| Orca (`stablyai/orca`) | `84a992a28a44` | `cloud/apps/relay/src/assignment` (cell placement), `src/main/runtime/orchestration/coordinator/coordinator.ts` |
| t3code (`pingdotgg/t3code`) | `786cf6a7368f` | `apps/server/src/resourceTelemetry`, searched for placement |
| gopsutil | `v4.26.10` | `mem/mem_linux.go` (what "used" means) |

Why these CI runners: Jenkins assigns work from the controller to agents, so its load balancer is where a CI system
could weigh capacity, and the Least Load plugin is the one that does. Buildkite agents pull jobs and the server's
choice is closed; only the agent's side can be read. GitHub Actions runner scale sets were not read: a runner there
asks for a job, the service assigns it, and nothing on the runner's side weighs the machine (inferred from how runners
register; not checked in code).

## How each scores a node

### Kubernetes scheduler

- **Filter, then score.** `NodeResourcesFit` refuses a node whose pods already fill its `pods` allocatable ("Too many
  pods", `helper/resource_fit.go:136-145`) or whose requested CPU and memory do not fit. Slots are a filter, not a
  score.
- **LeastAllocated, the default.** For each resource, `(capacity - requested) / capacity`, scaled to 0-100, averaged
  with weights (`least_allocated.go:30-62`). Default resources: CPU and memory, weight 1 each
  (`apis/config/v1/defaults.go:34-35`, `236-242`). `requested` is the sum of the pods' declared requests, not their
  measured use (`resource_allocation.go:140-167`).
- **MostAllocated** is its mirror, packing nodes (`most_allocated.go:30-64`). **RequestedToCapacityRatio** maps each
  resource's used share through a piecewise-linear shape the operator gives, then averages with weights
  (`requested_to_capacity_ratio.go:49-79`).
- **NodeResourcesBalancedAllocation** prefers nodes whose CPU and memory shares are close to each other: score
  `(1 - std) * 100` of the shares, taken as the change the pod makes (`balanced_allocation.go:170`, `227-277`). It
  avoids stranding one resource; it does not measure free room.
- Plugin scores are summed with weights; `NodeResourcesFit` and `BalancedAllocation` weigh 1 each
  (`default_plugins.go:43`, `57`).
- **Measured load and staleness:** none in the scheduler. The node's freshness is a filter: a node whose kubelet has
  not reported within `NodeMonitorGracePeriod`, 50 s by default (`nodelifecycle/config/v1alpha1/defaults.go:46`), is
  tainted unreachable or not ready (`node_lifecycle_controller.go:75-92`) and new pods stay off it.
- **Memory nearly full:** the kubelet's hard eviction threshold `memory.available < 100Mi`
  (`kubelet/eviction/defaults_linux.go:22-27`) sets `MemoryPressure`, which taints the node
  `memory-pressure:NoSchedule` (`node_lifecycle_controller.go:95-96`): refused for new pods, not scored down.
- **Ties:** a random number per node (`schedule_one.go:764`), compared after the total score (`sorted_nodes.go:61-64`).

### Nomad

- **Score:** `computeFreePercentage` is `1 - allocated/available` for CPU and memory, after the node's reserved part
  (`funcs.go:234-250`). Spread scores `10^freeCPU + 10^freeMem - 2`, 0 to 18 (`funcs.go:284-295`); binpack is its
  inverse (`257-277`); `spread_cpu_only` drops memory (`302-306`). Binpack is the default (`rank.go:197-206`). The
  fit is normalized by 18 (`rank.go:784-787`) and averaged with the other scorers (job anti-affinity, rescheduling
  penalty, affinities, spread) (`rank.go:1024-1037`).
- **Slots:** none; allocations are fitted by resources (`AllocsFit`, `rank.go:754`).
- **Measured load and staleness:** none in scoring; only allocated resources. A node is a candidate only when
  `Ready()`: status ready, no drain, eligible (`structs.go:2168-2170`, `scheduler/util.go:51`); the status comes from
  its heartbeats.
- **Ties:** the node list is shuffled with a seed from the evaluation id (`feasible.go:131-140`, `stack.go:82`); only
  `max(2, ceil(log2 n))` nodes are scored for a service job (`stack.go:85-99`); the first with the strictly highest
  score wins (`select.go:97-112`).

### Trimaran (Kubernetes scheduler-plugins)

The one scheduler read that scores by measured load.

- **TargetLoadPacking** reads the node's CPU utilisation percent from a load watcher (`targetloadpacking.go:131-144`)
  and scores toward a target utilisation. **LoadVariationRiskBalancing** scores `(1 - (mean + margin * stdev) / 2)`
  of the measured use (`loadvariationriskbalancing/analysis.go:34-59`).
- **Staleness:** metrics are refreshed every 30 s (`collector.go:33`, `90`) and the agent reports every 60 s
  (`handler.go:38-41`).
- **No metrics for a node:** it is not refused; it gets the minimum score, "Avoid the node by scoring minimum"
  (`targetloadpacking.go:107-117`; the same in `loadvariationriskbalancing.go:88-93`).
- It runs beside `NodeResourcesFit`, whose filter still refuses full nodes.

### Jenkins Least Load plugin

- **Score:** executors are sorted so that idle nodes (no busy executor) come first, then by the absolute number of
  idle executors, the most first (`LeastLoadBalancer.java:443-470`). No share of the limit, no memory, no CPU.
- **Ties:** the candidates are shuffled when more than one work unit is mapped, then sorted stably
  (`LeastLoadBalancer.java:347-361`).
- **Staleness:** only nodes online and not offline are candidates (`isAvailable`, `LeastLoadBalancer.java:352`).
- Of the CI dispatchers read, the only one that weighs capacity, and only by free executors.

### Buildkite agent

- The server assigns a job to an agent of the queue with the highest priority (`agent_start.go:391`). On a host with
  `--spawn`, `--spawn-with-priority descending` gives the n-th agent priority `-n` "so jobs are assigned across all
  hosts" (`agent_start.go:674`, `1271-1289`): the host with the fewest busy agents tends to win, by count. No memory or
  CPU. The server's own choice among equal priorities is closed source.

### Orca

- **Local coordinator:** `slotsAvailable = maxConcurrent - dispatched` for the whole app and the first available
  terminal takes the task (`coordinator.ts:230-272`). One machine; nothing to weigh.
- **Cloud relay cells** (its multi-machine placement): a cell is a candidate when admitted, below its capacity and
  live, with a heartbeat within `heartbeatTtlMs`, 45 s by default (`assignment-store.ts:659`, `8129-8154`). Score:
  `loadRatio = (reserved + observed requests) / capacity`, lowest first, ties by cell id
  (`assignment-store.ts:8156-8165`). An optional load band treats cells within 5% of the least loaded as equals and
  spreads among them (`placement-load-band.ts:1-12`). No memory or CPU of the machine.

### t3code

- It measures the host's CPU and `MemAvailable` for its diagnostics (`HostResources.ts:141-147`) but places no work:
  the person chooses the environment. Searched for `loadavg`, `freemem`, placement and least-loaded selection; found
  nothing that picks a machine.

## Comparison

| | Free share of slots | Free memory | CPU | Measured load | Stale data | Ties |
|---|---|---|---|---|---|---|
| Kubernetes LeastAllocated | slots a filter only | share of allocatable, by requests | share, by requests | no | node not ready after 50 s: refused | random |
| Kubernetes eviction and taints | | `< 100Mi` available: refused | | yes, kubelet | | |
| Nomad spread | no | share, by allocations | share, by allocations | no | node not ready: refused | shuffled order, first best |
| Trimaran | filter (Fit) | (LVRB optional) | measured percent | yes | none: minimum score | framework's random |
| Jenkins Least Load | idle first, then absolute idle executors | no | no | no | offline: refused | shuffle, stable sort |
| Buildkite | priority by spawn index | no | no | no | (server) | (server, closed) |
| Orca relay | `(reserved + observed) / capacity` | no | no | observed requests | heartbeat > 45 s: refused | cell id |
| t3code | none | measured, shown only | measured, shown only | shown only | | |
| Hypomnema today | absolute count | no | no | stored, unused | not connected (30 s): refused | id |

## Findings

- Every reference that weighs several resources combines them into one score with weights (Kubernetes
  `least_allocated.go:30-47`, Nomad `funcs.go:284-295`); none breaks ties by a second live measurement. Measured CPU
  and memory are real numbers that almost never tie, so a "tie-break by memory, then CPU" would in practice be decided
  by memory alone, or never reached.
- Scoring by free share alone does not fix the zm case: `elitex` 0/1 and `stc-hy-app` 0/4 are both 1.0, and with the
  issue's "1 of 4" `elitex` still wins (1.0 against 0.75). Free share plus measured CPU and memory, averaged as
  LeastAllocated does, picks `stc-hy-app` in both: `elitex` (1 + 0.002 + m)/3 ≤ 0.67, `stc-hy-app` at least
  (0.75 + 0.999 + 0.8)/3 ≈ 0.85, for any `elitex` memory share m.
- Freshness is a filter in Kubernetes, Nomad, Jenkins and Orca, and it is the node's heartbeat, which Hypomnema already
  checks. Trimaran, the only one scoring by measurement, does not refuse a node without metrics; it scores it lowest.
- Refusing a node for nearly full memory is the kubelet's (absolute threshold, 100 MiB); no scheduler read refuses by
  CPU.
- Ties: Kubernetes, Nomad and Jenkins randomise; Orca's relay and Hypomnema today order by id. Jenkins prefers more
  free executors in absolute count, which favours the larger machine.

The design that follows from this is in issue #731.
