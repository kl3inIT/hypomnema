# Vision

Intent, not fact. What exists is in [ARCHITECTURE.md](../ARCHITECTURE.md); the evidence behind each choice is in
[research](research/2026-11-06-platform.md).

## Goal

A personal software factory for one owner: one coordinator, many hands.

- The owner assigns work from a phone or a laptop through a web chat first; Telegram and a Flutter app later.
- A coordinator plans the work, splits it into tasks, and dispatches them to coding agents.
- The agents are the vendor CLIs, Claude Code and Codex, running on many machines across many clouds: VPS, EC2,
  Kubernetes, disposable sandboxes.
- Work ends in a pull request that passes CI. The owner approves the plan and the merge.
- One shared memory (MemoryOS) and one personal OS (Northstar), both over MCP; one vault for every secret.

## Target architecture

Two deployables, all Go:

| Deployable | Runs | Owns |
|---|---|---|
| `hypomnema` (control plane) | One place, public TLS endpoint or Tailscale | Channels, the coordinator, Run → Task → Dispatch → Gate as an event log in SQLite, the client API with policy (CLI, web; MCP later), node registry and placement, the vault (secrets and credential sessions), the API |
| `hypomnema-node` | Every machine, cloud and sandbox | Outbound connection, worktrees, running and parsing the CLIs, egress proxy that attaches credentials, process isolation, local journal |

- **The coordinator is pi** (`earendil-works/pi`), run as a child process over `pi --mode rpc`, not forked. It is a
  coordinator in Orca's shape: it decides, and the control plane holds every piece of state, so a pi crash loses
  nothing. It keeps pi's own tools but edits no code, and drives orchestration with the `hypomnema` CLI and a
  coordinator skill, as Orca's coordinators drive `orca orchestration` (decided 2026-11-09, issue #41); every policy
  decision is made in Go behind the client API. The owner's own agent on a laptop coordinates the same way.
- **Workers are the vendor CLIs**, driven headless: Codex through `codex app-server` (JSON-RPC), Claude Code through
  bidirectional stream-JSON with permission prompts answered by the host. pi can also be a worker for small cheap
  jobs.
- **Nodes dial out.** No machine opens an inbound port. SSH is only how the owner installs a node (a one-time token,
  as Omnara's installer); no work runs over SSH.
- **Credentials are per Dispatch.** The node's proxy attaches credentials from a vault session opened for one
  Dispatch and revoked when it closes, as Infisical's Agent Vault. The vault lives in the control plane; it moves to
  its own host only when the control plane has to run somewhere less trusted.
- **A worker that is a vendor's CLI talks to its vendor directly** (Claude Code, Codex), with its subscription and
  a token the control plane hands it for the Dispatch, as Orca, Omnigent and OpenHands run theirs. Its usage comes
  from the CLI's own report, and the account's quota from the vendor's usage endpoint. Until 2026-11-10 the intent
  was that this traffic also passed through the gateway.
- **One model gateway** in the control plane serves everything else: pi, workers that are not a vendor's CLI and the
  owner's own tools ([design](https://github.com/kl3inIT/hypomnema/issues/20)). It is an OpenAI-compatible API over a
  declared model table, with one adapter per kind of provider, translation through one hub format when the upstream
  speaks another, an account pinned per session, and usage recorded per key.
- **One store of accounts, two uses.** The owner adds an API key or a subscription (Claude, Codex, Antigravity) once
  in the web client; the secret is kept by the vault, and only the control plane refreshes a subscription's refresh
  token. An account can be handed to workers that run a vendor CLI directly (Claude Code with a `claude setup-token`
  token, Codex through `codex app-server` with tokens the control plane refreshes), served by the gateway through an
  adapter that builds the request as the vendor's client does, or both. The owner is the only user and accepts that
  a vendor may block an account used this way; until 2026-11-10 the intent was that a Claude subscription is used
  only by Claude Code and that the gateway never builds a request with a subscription.
- **One machine is enough to start**, with three OS users that each hold only their own: `hypomnema` (control plane
  and vault, the root key), `coordinator` (pi with all its tools, a filtered environment and its own directory), `agent`
  (a node running workers). More machines only add nodes. The coordinator spawns workers by itself within hard limits, as
  OpenClaw, Omnara and Omnigent do.
- **Shared infrastructure** for every node and worker comes later: a browser service, versioned skills pulled by
  nodes, memory stores read and written through tools, alongside the vault and the gateway.
- **Clients talk only to the control plane**: a web chat first, built for the phone as Omnigent's web UI is;
  Telegram and a Flutter app later. All use one OpenAPI contract, with events over SSE.
  The web client is a Vite single-page app: React, TanStack Router and Query, Tailwind and shadcn on Base UI
  ([stack](research/2026-11-09-directories-sessions-web-stack.md#4-the-web-stack)).

## What it learns from

Orca for the orchestration model (Run, Task, Dispatch, Gate, mailbox, fencing), Omnara for durability (agents as
event logs, leases, reapers, outbound daemons), Coder for the transition matrix, the model gateway and prebuilt pools, OpenClaw for the coordinator's tool surface and delegation (`sessions_spawn`,
announce, MCP bridge), Omnigent and durable-harness for driving CLIs and replay policy, Infisical for the vault,
second-bite for the Go stack, Omnara, Grafana, Teleport and Nomad for the layout and its checks. Details and paths are
in the research, including [workers, accounts and the gateway](research/2026-11-07-workers-accounts-gateway.md).

## Build order

One piece at a time, each usable on its own: spikes; a skeleton of the two deployables on one machine; the first
merged PR from a web chat message; a second cloud; task graphs and review stations; machine pools, Telegram and mobile.
Each step is a GitHub issue.
