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

## Second study: capacity by requests, and sharing room between Bots

Studied on 2026-11-17 for the two parts the owner added to issue #731 that day: a node's room computed from the
requests of its Dispatches against its allocatable, so that `max_dispatches` becomes an optional cap; and room shared
between Bots, with a guaranteed share, borrowing of free room, freed room going first to the Bot furthest below its
guarantee, and no preemption. The scoring above was approved the same day and is built in its own Task; this study
builds on it.

### What Hypomnema does today (main `03e5c21`)

- **Capacity is reported already.** The hello carries the machine's CPU count (`runtime.NumCPU`) and total memory
  (`internal/node/system.go:20-32`), stored as `nodes.cpus` and `nodes.memory_bytes` (`nodes.go:500`, columns in
  `migrations/00016_node_registry.sql:26-27`). The task's premise that nodes do not report their cores is wrong.
- **What is in use** comes every 10 s and replaces the last measurement (`NodeStats`, `nodes.go:548-575`). No history
  of it is kept, and nothing records what one Dispatch used.
- **A node's limit** is `max_dispatches`, `NOT NULL`, default 2 (`limits.go:81-82`), checked in `checkNode`
  (`placement.go:157`) and `atLimit` (`placement.go:271-276`).
- **A Bot's limit** (#660) is enforced in `scheduleTask` before any node is looked at: `p.groups[group] >= limit` waits
  with `bot_at_limit:<bot>` (`scheduler.go:82-85`). The group and its limit come from `runGroup` (`scheduler.go:
  275-286`): the Bot's `max_dispatches` (`bots.go:108-114`, column from `migrations/00100_limit_per_bot.sql:5`), else
  `BotDispatchesDefault` 4 (`limits.go:80`), and `""` for the owner's Runs without a Bot. The counts are the unsettled
  Dispatches per group (`placement.go:58`). The reason is not escalated (`scheduler.go:478`).
- **Order:** one pass takes every ready Task, oldest Run first, then by position
  (`ListSchedulableTaskIDs`, `internal/store/nodes.sql.go:727-732`); there is no order between Bots.
- A Dispatch that waits for an answer is unsettled, so it keeps its place (`activeDispatchStatuses`,
  `placement.go:473-481`; `transitions.go:49-52`). A Conversation that continues goes to its own node, and waits
  `nodes_full` when that node is at its limit (`scheduler.go:363-375`).
- The seeded profiles are `implementer`, `reviewer`, `explorer` and `coordinator`
  (`migrations/00008_profiles.sql:63-65`); a profile version has a runtime, model and tools but no resources
  (`internal/store/models.go:622-639`).

### Revisions read

Cloned into `.tmp/` with `--depth 1 --filter=blob:none --sparse` on 2026-11-17.

| Reference | Revision | What was read |
|---|---|---|
| Kubernetes | `f9951a0479e3` | `pkg/scheduler/framework/plugins/helper/resource_fit.go`, `noderesources/resource_allocation.go`, `pkg/scheduler/util/pod_resources.go`, `pkg/kubelet/cm/{helpers,node_container_manager_linux}.go`, `pkg/kubelet/nodestatus/setters.go`, `pkg/kubelet/eviction/defaults_linux.go`, `staging/src/k8s.io/kubelet/config/v1beta1/types.go`, `pkg/apis/scheduling/types.go`, `defaultpreemption/default_preemption.go` |
| Nomad | `359056d76692` | `nomad/structs/{funcs,structs,operator,eval}.go`, `nomad/config.go`, `client/config/config.go`, `client/fingerprint/{cpu,memory}.go`, `command/agent/config.go`, `client/allocrunner/taskrunner/stats_hook.go` |
| Kueue | `13eb8427416b` | `apis/kueue/v1beta2/clusterqueue_types.go`, `pkg/cache/scheduler/{resource_node,clusterqueue_snapshot,fair_sharing}.go`, `pkg/scheduler/scheduler.go`, `pkg/resources/requests.go` |
| Hadoop YARN | `aff670a9b190` | Capacity Scheduler: `policy/PriorityUtilizationQueueOrderingPolicy.java`, `AbstractParentQueue.java`, `AbstractLeafQueue.java`, `CapacitySchedulerConfiguration.java`, `conf/QueueProperties.java`; NodeManager `util/NodeManagerHardwareUtils.java`; `yarn-default.xml`, `YarnConfiguration.java` |
| Slurm | `9adf0e0c479b` | `src/plugins/priority/multifactor/{priority_multifactor,fair_tree}.c`, `doc/man/man5/slurm.conf.5` |

Paths below are relative to each clone; the YARN ones to
`hadoop-yarn-server-resourcemanager/.../scheduler/capacity/` unless they name another module.

### Capacity and allocatable

- **Kubernetes.** The kubelet's allocatable is capacity minus `system-reserved`, `kube-reserved` and the hard
  eviction thresholds (`GetNodeAllocatableReservation`, `pkg/kubelet/cm/node_container_manager_linux.go:292-313`;
  `hardEvictionReservation`, `cm/helpers.go:121-122`), floored at zero (`nodestatus/setters.go:310-320`). Both
  reserves default to nil (`kubelet/config/v1beta1/types.go:765-782`); the eviction default is `memory.available
  100Mi` and disk thresholds, with no CPU signal (`eviction/defaults_linux.go:22-28`). So by default a node's
  allocatable memory is its total less 100 MiB, and its CPU is all of it.
- **Nomad.** The client fingerprints total memory with gopsutil's `VirtualMemory().Total`, the library Hypomnema's
  node uses (`client/fingerprint/memory.go:30-50`), and total compute from the CPU topology (`cpu.go:33-49`).
  `reserved { cpu, memory, ... }` in the client config is subtracted (`command/agent/config.go:347-350`); it defaults
  to empty (`command/agent/config.go:1840`). `AllocsFit` checks `NodeResources - ReservedResources` against the sum of
  the allocations (`nomad/structs/funcs.go:192-198`).
- **YARN.** With hardware detection, the NodeManager offers `0.8 * (physical - 2 * its heap)` of memory, or physical
  minus `system-reserved-memory-mb` when that is set (`NodeManagerHardwareUtils.java:310-330`), and vcores =
  physical cores times a multiplier (`:215-235`).
- **Slurm.** `MemSpecLimit` and `CoreSpecCount` reserve memory and cores for the system; unset by default
  (`slurm.conf.5:8649-8652`).
- **Findings.** Kubernetes, Nomad and Slurm reserve nothing by default beyond Kubernetes' 100 MiB eviction floor;
  YARN alone keeps 20%. All four let the operator set a reserve per node. Kubernetes folds its memory floor into
  allocatable, which matches the 1 GiB floor already approved for #731.

### Requests

- **Kubernetes.** Requests are declared per container; `FitsRequest` refuses a node when one more pod exceeds the
  `pods` allocatable ("Too many pods", `helper/resource_fit.go:136-145`) or when the pod's CPU or memory is more than
  `allocatable - requested` (`:159-178`). A request larger than the whole allocatable is marked `Unresolvable`
  (`:166`, `:176`): no node change of load can help. `requested` is the sum of the pods' declared requests, not their
  use; scoring takes the same sums (`noderesources/resource_allocation.go:200-210`). A pod with no request is scored
  as 0.1 CPU and 200 MiB (`pkg/scheduler/util/pod_resources.go:28-31`).
- **Nomad.** A task's `resources` default to 100 MHz and 300 MB (`DefaultResources`, `nomad/structs/structs.go:
  2388-2394`). What was allocated is kept on the allocation and summed (`funcs.go:157-164`), so a later change of the
  job does not change what a running allocation counts. `node_max_allocs` is an optional count cap, 0 meaning none
  (`client/config/config.go:400-402`, `structs.go:2128-2129`, checked first in `AllocsFit`, `funcs.go:145-149`).
  The client samples each task's use (`stats_hook.go:100-117`); the scheduler does not read it.
- **YARN** and **Slurm** take requests per container or job (Slurm's `DefMemPerCPU` is 0, unlimited, by default:
  `slurm.conf.5:1421-1426`).
- **Findings.** Every scheduler fits on declared requests, never on measured use, and keeps the request on the
  running unit. Nomad has exactly the optional count cap the issue asks for. None derives defaults from history in
  the scheduler (Kubernetes' Vertical Pod Autoscaler does, as a separate component; not read).

### Guarantees, borrowing and ordering between owners of work

- **Kueue.** A ClusterQueue has a `nominalQuota` per resource, guaranteed (`clusterqueue_types.go:318-329`); it may
  borrow unused quota of its cohort up to `borrowingLimit`, nil meaning no limit (`:331-341`), and lends up to
  `lendingLimit`, nil meaning all of it (`:343-353`). `available` adds the queue's local quota to what its parent has
  left, bounded by the borrowing limit (`pkg/cache/scheduler/resource_node.go:119-134`). Quota may count pods
  (`pkg/resources/requests.go:35`). Order (classic): workloads that already hold quota, then those that fit under
  their nominal quota before those that borrow, then priority, then FIFO (`pkg/scheduler/scheduler.go:1277-1345`).
  With fair sharing, a tournament by dominant resource share, the borrowed part over what the cohort can lend, divided
  by a weight (`pkg/cache/scheduler/fair_sharing.go:166-194`). `queueingStrategy` defaults to `BestEffortFIFO`: an
  older workload that cannot be admitted does not block newer ones that fit; `StrictFIFO` blocks
  (`clusterqueue_types.go:98-110`).
- **YARN Capacity Scheduler.** Each queue has a `capacity` (guarantee) and a `maximum-capacity`, 100% by default
  (`conf/QueueProperties.java:255-267`), so a queue may grow to the whole cluster when others are idle. Children are
  sorted by used capacity relative to their own guarantee, lowest first (`PriorityUtilizationQueueOrderingPolicy.java:
  69-74`; priorities only matter when both are on the same side of their guarantee, `:75-87`); the sort is redone for
  every container (`:251-259`), and the parent gives one container to the first child that takes one, then sorts
  again (`AbstractParentQueue.java:962-990`). A queue with nothing pending is skipped (`AbstractLeafQueue.java:
  1199-1212`).
- **Slurm** (fair share between accounts and users). `priority/multifactor` orders jobs by, among other factors,
  fair share: classic `2^-(usage/shares)` (`priority_multifactor.c:1865-1886`), or Fair Tree's `level_fs = S/U`,
  highest first (`fair_tree.c:199-210`, `:140-154`), with usage decayed over a 7-day half-life
  (`slurm.conf.5:3665-3676`). It weighs past use, not what runs now.
- **Nomad** has no fair share in its open-source code: quotas are an Enterprise feature, with only the fields left
  (`nomad/structs/structs.go:5587-5589`, `eval.go:200-202`). Searched `nomad/structs` and `scheduler` for quota and
  share logic; none.
- **Findings.** "Room goes first to the one furthest below its guarantee" is YARN's rule exactly: lowest used over
  guarantee first, re-sorted after each placement, queues with nothing pending skipped. Kueue's classic order gives
  the same split at the guarantee (under nominal before borrowing) and adds FIFO inside. "No cap below the total" is
  YARN's default `maximum-capacity` and Kueue's nil `borrowingLimit` and `lendingLimit`. Slurm's decayed history
  answers a different question (fairness over days) and needs usage accounting Hypomnema does not have.

### Preemption

- Kueue: `reclaimWithinCohort` and `withinClusterQueue` default to `Never` (`clusterqueue_types.go:585-628`), as does
  `borrowWithinCohort` (`:631-653`).
- YARN: preemption is a monitor, off by default (`yarn.resourcemanager.scheduler.monitor.enable` false,
  `yarn-default.xml:1038-1043`; `YarnConfiguration.java:348`).
- Nomad: preemption is on for system jobs only, off for service and batch (`nomad/config.go:709-714`;
  `nomad/structs/operator.go:339-351`).
- Slurm: `PreemptType` is unset by default (`slurm.conf.5:3603-3605`).
- Kubernetes: preemption is on by default, and a pod opts out with `preemptionPolicy: Never` (`pkg/apis/scheduling/
  types.go:52-59`; `defaultpreemption/default_preemption.go:459`).
- **Finding.** With preemption off, a guarantee in Kueue and YARN is a first claim on room as it frees, not room kept
  empty: a queue that lent everything waits for the borrower's work to finish. This is the behaviour the owner asked
  for.

### Measurements

**History.** The worker that did this study had no client token (no Hypomnema credential among its environment
variables), so it could not read Dispatches. Even with one, the control plane keeps only each node's latest
measurement (`nodes.go:548-575`); it records which profile ran on which node and when (`dispatches.node_id`,
`started_at`, `settled_at`; `tasks.profile`), never the CPU or memory of a Dispatch. The owner can count profile by
node, read-only, on the control plane's database:

```sql
SELECT t.profile, d.node_id, count(*) AS n,
       round(avg((d.settled_at - d.started_at) / 60000.0), 1) AS avg_min
FROM dispatches d JOIN tasks t ON t.id = d.task_id
WHERE d.started_at IS NOT NULL AND d.settled_at IS NOT NULL
GROUP BY 1, 2 ORDER BY 1, 3 DESC;
```

**Measured here instead.** On `stc-hy-app` (24 CPUs, 31.4 GiB), 2026-11-17, Hypomnema at `03e5c21`, Go 1.27.4: the
repository's own checks, the work an implementer or a reviewer of this repository runs. A sampler read `MemAvailable`
and `/proc/stat` every 0.5 s (script `.tmp/scripts/measure.sh`, not kept); memory is the largest drop of
`MemAvailable` during the command, so it counts the whole process tree. Three other workers (two Codex, one Claude)
were idle on the machine; the Go build cache was emptied before the cold build.

| Command | Wall | Mean CPUs | Peak CPUs (0.5 s) | Peak memory |
|---|---|---|---|---|
| `go build ./...`, empty cache | 70 s | 8.4 | 23.6 | 2.6 GiB |
| `go build ./...`, warm cache | 8 s | 2.4 | 6.4 | 0.4 GiB |
| `go test -count=1 ./internal/controlplane/orchestration/...` | 99 s | 5.4 | 19.6 | 0.6 GiB |
| `go test -count=1 ./...` | 100 s | 7.2 | 22.2 | 1.7 GiB |
| `golangci-lint run ./...` (`go tool -modfile=tools/ci/go.mod`) | 43 s | 12.4 | 23.2 | 2.7 GiB |

Resident memory of the worker CLIs between commands (`ps`, summed per process group): Claude Code 269 MiB, Codex
156-172 MiB (and 23 MiB for `codex-code-mode`), the node's shim per worker 10-27 MiB. Not measured: the web client's
`pnpm check` (this node has Node 22, the client needs 24) and the owner's other repositories (a JDK and Gradle build
is likely heavier; inferred).

What the numbers say: a worker's memory peaks at about 3 GiB (2.7 GiB of checks plus its CLI) and its CPU use is
elastic, every core in bursts and a few on average; Go and the linter size their parallelism to the cores they find,
so the same work on one core is slower, not refused. Memory is the dimension that can break a machine; CPU only slows
it (inferred from the kubelet having a memory eviction signal and none for CPU, `defaults_linux.go:22-28`).

### Comparison

| | Capacity | Reserved by default | Request default | Count cap | Guarantee | Borrowing | Order of freed room | Preemption by default |
|---|---|---|---|---|---|---|---|---|
| Kubernetes | node status capacity | eviction 100 MiB | none (scored as 0.1 CPU, 200 MiB) | `pods` allocatable | — | — | queue priority, FIFO | on; `Never` per pod |
| Nomad | fingerprint (gopsutil) | none | 100 MHz, 300 MB | `node_max_allocs`, 0 = none | Enterprise quotas | — | eval priority | system jobs only |
| Kueue | `nominalQuota` (cluster-wide) | — | pod requests | quota on `pods` | `nominalQuota` | up to `borrowingLimit`, nil = none | under nominal first, then FIFO; or lowest DRS | `Never` |
| YARN | NodeManager detection | 20% (or set) | per container | — | `capacity` | up to `maximum-capacity`, 100% | lowest used / guarantee, re-sorted each container | monitor off |
| Slurm | `RealMemory`, CPUs | none | `DefMemPerCPU` 0 | — | shares | — | fair share by decayed past use | none |
| Hypomnema today | `cpus`, `memory_bytes` stored, unused | — | — | `max_dispatches`, required | — (Bot limit is a cap) | no | oldest Run first | none |

The design that follows from this is the second `## Design` comment on issue #731.
