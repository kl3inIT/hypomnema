# Vision

Intent, not fact. What exists is in [ARCHITECTURE.md](../ARCHITECTURE.md); the evidence behind each choice is in
[research](research/2026-11-06-platform.md).

## Goal

A personal software factory for one owner: one coordinator, many hands.

- The owner assigns work from a phone (Telegram first, later web and mobile).
- A coordinator plans the work, splits it into tasks, and dispatches them to coding agents.
- The agents are the vendor CLIs, Claude Code and Codex, running on many machines across many clouds: VPS, EC2,
  Kubernetes, disposable sandboxes.
- Work ends in a pull request that passes CI. The owner approves the plan and the merge.
- One shared memory (MemoryOS) and one personal OS (Northstar), both over MCP; one vault for every secret.

## Target architecture

Two deployables, all Go:

| Deployable | Runs | Owns |
|---|---|---|
| `hypomnema` (control plane) | One place, public TLS endpoint or Tailscale | Channels, the coordinator, Run → Task → Dispatch → Gate as an event log in Postgres, MCP bridge with policy, node registry and placement, the vault (secrets and credential sessions), the API |
| `hypomnema-node` | Every machine, cloud and sandbox | Outbound connection, worktrees, running and parsing the CLIs, egress proxy that attaches credentials, process isolation, local journal |

- **The coordinator is pi** (`earendil-works/pi`), run as a child process over `pi --mode rpc`, not forked. It is a
  coordinator in Orca's shape: it decides, and the control plane holds every piece of state, so a pi crash loses
  nothing. Its tools come through MCP and a thin pi extension; every policy decision is made in Go.
- **Workers are the vendor CLIs**, driven headless: Codex through `codex app-server` (JSON-RPC), Claude Code through
  bidirectional stream-JSON with permission prompts answered by the host. pi can also be a worker for small cheap
  jobs.
- **Nodes dial out.** No machine opens an inbound port. An SSH executor exists only to bootstrap a node or to reach a
  trusted machine of the owner.
- **Secrets never reach a worker.** The node's proxy attaches credentials from a vault session opened for one
  Dispatch and revoked when it closes, as Infisical's Agent Vault. The vault lives in the control plane; it moves to
  its own host only when the control plane has to run somewhere less trusted.
- **One machine is enough to start**, with three OS users that each hold only their own: `hypomnema` (control plane
  and vault, the root key), `coordinator` (pi with all its tools, a filtered environment and its own directory), `agent`
  (a node running workers). More machines only add nodes. The coordinator spawns workers by itself within hard limits, as
  OpenClaw, Omnara and Omnigent do.
- **Clients talk only to the control plane**: Telegram first; a web chat and a Flutter app later, over one OpenAPI
  contract, with events over SSE.

## What it learns from

Orca for the orchestration model (Run, Task, Dispatch, Gate, mailbox, fencing), Omnara for durability (agents as
event logs, leases, reapers, outbound daemons), OpenClaw for the coordinator's tool surface and delegation (`sessions_spawn`,
announce, MCP bridge), Omnigent and durable-harness for driving CLIs and replay policy, Infisical for the vault,
second-bite for the Go stack, Omnara, Grafana, Teleport and Nomad for the layout and its checks. Details and paths are
in the research.

## Build order

One piece at a time, each usable on its own: spikes; a skeleton of the two deployables on one machine; the first
merged PR from a Telegram message; a second cloud; task graphs and review stations; machine pools, web and mobile.
Each step is a GitHub issue.
