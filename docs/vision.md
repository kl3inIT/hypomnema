# Vision

Intent, not fact. What exists is in [ARCHITECTURE.md](../ARCHITECTURE.md); the evidence behind each choice is in
[research](research/2026-11-06-platform.md).

## Goal

A personal factory for one owner: Bots that talk with the owner, many hands that do the work. It builds the owner's
software and also does the owner's own work that is not code, such as reading mail, looking something up or
preparing a document. Until 2026-11-10 the goal was a software factory only.

- The owner assigns work from a phone or a laptop through the web chat or a Lark chat; Telegram and a Flutter app
  later.
- A coordinator plans the work, splits it into tasks, and dispatches them to workers. The coordinator is a part in a
  Run, not one program: a Bot takes it, or the owner's own agent on a laptop (owner, 2026-11-11: until then pi on the
  server was the one coordinator).
- The workers are coding agents' CLIs (Claude Code, Codex, omp, opencode) running on many machines across many
  clouds: VPS, EC2, Kubernetes, disposable sandboxes.
- Work on code ends in a pull request that passes CI. The owner approves the plan and the merge.
- Other work is the same kind of task, as in OpenClaw, Hermes and Omnigent: no repository, other tools, and a report
  to the owner with what it produced instead of a pull request
  ([research](research/2026-11-10-git-hosts-and-personal-work.md)). A Bot also does work on a schedule, as a
  Routine ([research](research/2026-11-12-work-on-a-schedule.md)).
- Each git host is one adapter, found from the repository's URL, as Orca, t3code and Omnigent have one a host;
  nothing else knows the host. GitHub and GitLab are the first two.
- Agents and Bots keep their memory in the control plane, each its own and one about the owner that all read
  ([research](research/2026-11-13-agent-memory.md); owner, 2026-11-13: MemoryOS until then); one personal OS
  (Northstar) over MCP; one vault for every secret.

## Target architecture

Two deployables, all Go:

| Deployable | Runs | Owns |
|---|---|---|
| `hypomnema` (control plane) | One place, public TLS endpoint or Tailscale | Channels, the Bots and their Routines, Run → Task → Dispatch → Gate as an event log in SQLite, the client API with policy (CLI, web; MCP later), node registry and placement, the vault (secrets), the model gateway, the service proxy and the shared infrastructure below |
| `hypomnema-node` | Every machine, cloud and sandbox | Outbound connection, worktrees, running and parsing the CLIs, process isolation, local journal |

- **A Bot runs pi** (`earendil-works/pi`), one child process per chat over `pi --mode rpc`, not forked. A Bot that
  coordinates is a coordinator in Orca's shape: it decides, and the control plane holds every piece of state (the
  chat, the Runs, the Bot's memory), so a pi crash loses nothing. It keeps pi's own tools but edits no code, and
  drives orchestration with the `hypomnema` CLI and a coordinator skill, as Orca's coordinators drive `orca
  orchestration` ([decision](decisions/2026-11-09-coordinator-cli-and-skill.md), issue #41); every policy decision
  is made in Go behind the client API. The owner's own agent on a laptop coordinates the same way. Several Bots,
  each with its persona, memory and tools, as OpenMausBot's bots and OpenClaw's agents (issue #169).
- **Workers are coding agents' CLIs**, driven headless: Codex through `codex app-server` (JSON-RPC), Claude Code
  through bidirectional stream-JSON with permission prompts answered by the host, omp and opencode over ACP. pi is
  the Bots' and is not a worker; small cheap jobs go to omp or opencode on a model of the gateway.
- **Nodes dial out.** No machine opens an inbound port. SSH is only how the owner installs a node (a one-time token,
  as Omnara's installer); no work runs over SSH.
- **Credentials are per Dispatch.** An Integration's token stays in the control plane, behind its service proxy; a
  worker's Claude and git host tokens go to its Dispatch alone, as environment, with no proxy on the node
  ([decision, 2026-11-13](decisions/2026-11-13-worker-credentials-in-the-environment.md): until then a proxy as
  Infisical's Agent Vault). The vault lives in the control plane; it moves to its own host only when the control
  plane has to run somewhere less trusted.
- **A worker that is a vendor's CLI talks to its vendor directly** (Claude Code, Codex), with its subscription and
  a token the control plane hands it for the Dispatch, as Orca, Omnigent and OpenHands run theirs. Its usage comes
  from the CLI's own report, and the account's quota from the vendor's usage endpoint. Until 2026-11-10 the intent
  was that this traffic also passed through the gateway.
- **One model gateway** in the control plane serves everything else: pi, workers that are not a vendor's CLI and the
  owner's own tools ([decision](decisions/2026-11-10-model-gateway.md)). It is an OpenAI-compatible API over a
  declared model table, with one adapter per kind of provider, translation through one hub format when the upstream
  speaks another, an account pinned per session, and usage recorded per key.
- **One store of accounts, two uses.** The owner adds an API key or a subscription (Claude, Codex, Antigravity) once
  in the web client; the secret is kept by the vault, and only the control plane refreshes a subscription's refresh
  token. An account can be handed to workers that run a vendor CLI directly (Codex through `codex app-server` with
  tokens the control plane refreshes), served by the gateway through an adapter that builds the request as the
  vendor's client does, or both. A Claude subscription is only served by the gateway: a Claude Code worker calls it
  with its Dispatch's key, and the token never leaves the control plane
  ([decision](decisions/2026-11-14-claude-code-workers-only-through-the-gateway.md)). The owner is the only user and accepts that
  a vendor may block an account used this way; until 2026-11-10 the intent was that a Claude subscription is used
  only by Claude Code and that the gateway never builds a request with a subscription.
- **One machine is enough to start**, with three OS users that each hold only their own: `hypomnema` (control plane
  and vault, the root key), `coordinator` (pi with all its tools, each chat's pi in a bubblewrap box of its own, as
  OpenMausBot's bot box), `agent` (a node running workers). More machines only add nodes. The coordinator spawns
  workers by itself within hard limits, as OpenClaw, Omnara and Omnigent do.
- **Shared infrastructure** for every node and worker lives in the control plane, alongside the vault and the
  gateway: one browser every agent shares, versioned skills that nodes fetch, memory read and written through
  tools, and web search and fetch. A node holding the browser instead comes later.
- **Clients talk only to the control plane**: a web chat, built for the phone as Omnigent's web UI is, and Lark as
  the first Channel; Telegram and a Flutter app later. All use one OpenAPI contract, with events over SSE.
  The web client is a Vite single-page app: React, TanStack Router and Query, Tailwind and shadcn on Base UI
  ([stack](research/2026-11-09-directories-sessions-web-stack.md#4-the-web-stack)).

## What it learns from

Orca for the orchestration model (Run, Task, Dispatch, Gate, mailbox, fencing), Omnara for durability (agents as
event logs, leases, reapers, outbound daemons) and skills, Coder for the transition matrix, the model gateway and
prebuilt pools, OpenClaw for the coordinator's tool surface and delegation (`sessions_spawn`, announce, MCP bridge),
OpenMausBot for Bots, their box, the browser and the team setup, Hermes for memory and integrations' skills,
Omnigent and durable-harness for driving CLIs and replay policy, t3code for the web client's look and the browser
pane, OmniRoute for web search, Infisical for the vault, second-bite for the Go stack, Omnara, Grafana, Teleport and
Nomad for the layout and its checks. Details and paths are in the [research](research/README.md), including
[workers, accounts and the gateway](research/2026-11-07-workers-accounts-gateway.md).

## Build order

One piece at a time, each usable on its own, each step a GitHub issue. Done so far (see
[ARCHITECTURE.md](../ARCHITECTURE.md)): the two deployables, a merged PR from a web chat message, a second cloud
(machines at AWS), task graphs and review profiles, Bots with Routines and a Lark Channel, and the owner's personal
work through Integrations (Google, Lark, Superset, AWS). Next: machine pools, Telegram and mobile, budgets per Run
and per gateway key.
