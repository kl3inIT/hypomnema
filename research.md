# Personal AI platform: research and naming

Date: 2026-11-06. Written for a new personal project, separate from MemoryOS.
Sources were read from shallow clones in this repository's `.tmp/` (gitignored; `block/buzz` at `4e28176`,
`omnara-ai/omnara` at `0ef1711`, both 2026-11-05) and from web searches. Anything not checked against code or a running
system is marked **(unverified)**.

Legend for the tables: ✅ supported and documented, ⚠️ partial or with a caveat, 🚧 planned, ❓ not checked, ❌ not
supported.

## 1. Goal

A personal AI platform for one user, in the spirit of MemoryOS:

- **One brain**: one shared memory and one secrets vault for every agent.
- **Many hands**: agents run on several servers (zm, other VPSs, disposable sandboxes).
- **Remote work assignment**: you message it (Telegram, Lark, X, web), agents do the work (code, research, operations)
  and report back. For code, the flow ends in a pull request through the same PR loop MemoryOS uses.
- **A personal software factory**: in the spirit of Peter Steinberger (OpenClaw) and Mastra Factory.

## 2. Pattern: the software factory

Also called *background agents*, *ambient agents* or *issue-to-PR*. The common flow:

1. **Trigger**: a tag on X, GitHub, Slack or Linear, a label on an issue, or cron.
2. **Agent**: reads the request, creates a branch, writes code, runs tests.
3. **Stations**: a review agent, CI, then fix and repeat.
4. **Output**: a PR, sometimes merged and deployed automatically. A human approves the plan and the merge, or nothing
   at all.

### Peter Steinberger (OpenClaw)

Source: the Pragmatic Engineer article "I ship code I don't read" (01/2026), plus one post on X.

- Runs **5 to 10 agents in parallel**, one feature each. More than 6,600 commits alone in 01/2026 **(unverified)**:
  quote the article before reusing the number.
- Plans thoroughly with the agent before letting it run. Prefers Codex for long work.
- **Code review replaced by architecture discussion**. "Pull requests are dead, long live prompt requests": an
  outside PR is treated as an idea to write as a prompt.
- **Agents must verify their own work**: build, lint, run. CI runs locally instead of remotely.
- Latest: "build OpenClaw with OpenClaw". The team moved to **one shared agent** that knows who is doing what and
  coordinates it all, with multiplayer coding on **nodes and cloud sessions**.
- **(unverified)**: that "tag the bot on X and it builds" is his workflow. OpenClaw's X plugin only replies to
  mentions.

### Open-source factories

| Project | How it works | Notes |
|---|---|---|
| Mastra Factory (beta 09/2026) | Issues from GitHub or Linear. Stages: Intake, Triage, Planning, Build, Review, Done; each stage manual or automatic | A human approves the plan and the merge. Remote sandboxes (Daytona, E2B, Blaxel). Self-reported: 277 of 1,627 merged PRs (17%) written by the factory |
| OpenFactory | Issue (GitHub, Linear, Sentry) → Claude Code or Codex → PR → review → merge | Self-hosted. Warns that issue text is untrusted input while the agent holds a GitHub token |
| Vercel eve / Foreman | Label on an issue → four agent stations → draft PR | Needs a GitHub App with write access |

## 3. Platforms compared

### 3.1 Two schools

**A. Their own harness, paid per API call**: Omnara, Hermes, OpenClaw, Mastra.
- Full control, any model, multi-machine, memory and vault.
- Costs per token. My judgement, not benchmarked: they write code worse than Claude Code or Codex, which each vendor
  tunes for its own models.

**B. Meta-harness: drives the Claude Code or Codex CLI on a subscription**: Buzz, Paseo, Omnigent,
Codex Orchestrator, Agora, Vibe Kanban, Orca.
- Uses the vendor's best harness and the subscription quota, which is far cheaper than the API for heavy work.
- Only orchestrates: worktrees, sandboxes, policy, shared rooms.
- **Terms-of-service risk**: driving a subscription login from an orchestrator, on several servers, may break the
  vendor's consumer terms, the same risk as a proxy such as 9Router. Check Anthropic's and OpenAI's terms before
  relying on it **(unverified)**. Buzz's own docs only document API keys (see 3.3).

**Read on the November 2026 trend**: for code, the vendor harness is winning and the meta-harness is becoming a layer
of its own. For non-code work (research, content, operations, cron), platforms with their own harness fit better.
This is my judgement from the sources above, not verified market data.

### 3.2 Comparison table

| | Buzz (Block) | Omnara | OpenClaw | Hermes Agent (Nous) |
|---|---|---|---|---|
| Kind | Workspace where humans and agents share rooms | Platform for running agents (API, CLI, SDK) | Personal assistant | Personal assistant |
| Harness | **ACP → Claude Code, Codex, goose, OpenCode** | Its own (Go worker) | **pi** embedded as the default runtime, plus CLI backends (Claude Code), the Codex app-server harness and ACP agents (3.3) | Its own |
| Subscription | ⚠️ `buzz-acp` README documents `OPENAI_API_KEY` ("not a ChatGPT subscription") and `ANTHROPIC_API_KEY`; subscription login not documented | ❌ API key only; can point at a proxy such as 9Router (watch the terms of service) | ✅ through the vendor runtimes: Claude Code CLI backend, Codex app-server ("the usual ChatGPT/Codex subscription setup") | ❓ |
| Many servers | ⚠️ `buzz-acp` is a headless daemon that connects to a relay over WebSocket, so it runs on any server; managed remote bodies are 🚧 (`buzz-backend-kubernetes`, mesh) | ✅ BYO machines (`omnarad`, outbound connection, no open ports) and pools of 12 sandbox providers | ✅ Gateway and nodes (pairing approval, SSH or Tailscale) | ⚠️ One SSH target per configuration |
| Shared memory | ⚠️ engrams (NIP-AE) are per agent–owner pair, encrypted to the owner; **not shared between agents** | ✅ memory stores `/memory/<store>/…`, files without history | ✅ at the Gateway | Local files (`MEMORY.md`); Honcho plugin |
| Vault | ⚠️ keypair per agent, secrets kept out of configuration | ✅ secrets scoped to org, project or user, `secret_env` | ❓ | `.env` per instance |
| Chat channels | The workspace itself (desktop; mobile in progress) | Slack, Discord, GitHub, custom | Telegram, X and many more | Telegram, Discord… |
| License, size (stars on 2026-11-06) | Apache 2.0, 36.8k stars | Apache 2.0, 3.4k stars | Open source, run by a foundation; about 394k stars | Open source; license ❓ |

### 3.3 Notes on each

**Omnara** (`.tmp/omnara/docs`):
- The control plane is the brain. An agent is an event log; a machine is only the body that executes. "The machine
  does not own the agent."
- Every machine runs `omnarad`, which connects outbound to the control plane and updates itself.
- Two kinds of machine:
  - **BYO**: an existing server, for internal networks, mounts, GPUs.
  - **Pool**: created on demand from Daytona, E2B, Modal, Blaxel, Unikraft and others, deleted when idle.
- An agent asks for more machines itself with `create_machine`. Access is granted per project.
- Self-hosting needs Postgres, Redis, blob storage and a memory filesystem; workers scale horizontally.

**Buzz** (`.tmp/buzz`):
- A Nostr relay: messages, reviews, workflow steps and git events are all signed events in one log, searchable and
  auditable.
- Agents are members with their own keys and their own permissions.
- A feature branch becomes a room: patch, CI, agent review and the merge decision in one place.
- YAML workflows triggered by a message, a reaction, a schedule or a webhook.
- The "Remote Agents" vision, "same agent, new body":
  - identity, memory and history live on the relay, and the machine is replaceable;
  - an agent shuts itself down when idle and returns on a new machine with the same memory;
  - the relay is the control plane.
- Today, as the vision puts it, an agent's "body is borrowed: it runs while a desktop app runs". That is the desktop
  path. Separately, `buzz-acp` (crate `crates/buzz-acp`) is a standalone CLI: given `BUZZ_PRIVATE_KEY` and
  `BUZZ_RELAY_URL` it connects to the relay, spawns `goose`, `codex-acp` or `claude-agent-acp` over stdio and answers
  @mentions. Up to 32 agent subprocesses per harness (`--agents`). No desktop needed.
- **Inbound author gate**: `--respond-to` defaults to `owner-only` (also `allowlist`, `anyone`, `nobody`). Events from
  other authors are dropped before they reach the agent. Owner commands: `!cancel`, `!rotate`, `!shutdown`.
- **Engrams** (`docs/nips/NIP-AE.md`, draft): addressable `kind:30174` events signed by the agent and encrypted with
  NIP-44 to the owner. Each agent has one `core` record (identity, rules, goals), loaded into the prompt at session
  start, plus `mem/…` records. Memory belongs to one agent–owner pair, so it carries the agent across machines
  but is **not a memory shared between agents**. The NIP itself warns about memory poisoning.

**OpenClaw**:
- The Gateway is one long-running process that holds the chat channels and the control plane over WebSocket.
- Nodes are machines that run commands: `openclaw node run --host <gateway>`, each approved with
  `openclaw devices approve`.
- Exec approvals are per node.
- **How it delegates** (sparse clone of `docs/` at `d622a2b`, 2026-11-06; read `concepts/agent-runtimes.md` §Runtime
  ownership and §Runtime selection, `gateway/cli-backends.md` §How it works and §Keeping Claude Code orchestration
  tools, `specs/codex-supervision.md`, `tools/acp-agents.md`, `tools/acp-agents/sessions.md`, `delivery.md`,
  `tools/subagents.md`):
  - Four layers: channel, provider, model, **agent runtime** (`openclaw`/pi, `claude-cli`, `codex`, `copilot`). The
    runtime is chosen per model (`agentRuntime.id`), so one turn can run on pi and the next on Claude Code.
  - **Sub-agents**: `sessions_spawn({task})` starts a background run in its own session, which **announces** its
    result back to the requester. A cheaper model can be set for sub-agents.
  - **ACP agents**: `sessions_spawn({task, runtime: "acp", agentId: "codex", mode: "run"})` runs a real Claude Code,
    Codex or Gemini CLI through `@openclaw/acpx`; one-shot runs report through the same announce path, persistent
    ones bind to a chat thread. The docs forbid turning this into a parent–child chat.
  - **CLI backends**: a turn runs on the installed Claude Code executable, kept warm across turns. Tools reach the CLI
    through a loopback **MCP bridge** with OpenClaw's policy and approvals; Claude's own file and shell tools are
    withheld. `Agent` and `Workflow` can be opted in; descendants still running when a turn ends have their process
    closed so they cannot act under a later turn's authority.
  - **Paired nodes**: an idle Codex thread on another machine can be continued with `codex exec resume`, gated by
    `operator.admin` and an explicit node-command allowlist.
- Maturity, self-reported (`docs/maturity/scorecard.md`): Agent Runtime M3 Beta; "External Runtimes and Subagents"
  had QA evidence for 3 of 10 features in the 2026-09-24 run. Open issues by title on 2026-11-06: `claude-cli` 129
  (496 closed), `subagent` 95 (1,080 closed), `acp` 49 (546 closed). Widely used; the user accepts it as proven.

**Others**:

| Project | What it does | Note |
|---|---|---|
| Paseo | Self-hosted daemon; runs Claude Code and Codex in parallel; CLI, web and mobile clients | Apache 2.0 |
| Omnigent | Meta-harness for Claude Code, Codex, Cursor and Pi; sandboxes and parallel worktrees | |
| Codex Orchestrator | Keeps a fleet of machines running Codex, Claude Code and Grok Build in sync: auth, configuration, skills | |
| Agora | Agents on many machines coordinate in shared channels | |
| Mainplane (11/2026) | Mesh control plane connecting agents to your machines | Vault and memory unclear |
| Infisical Agent Vault | Vault and credential proxy for agents: the agent never sees the real credential | Documented as a product in `docs/documentation/platform/agent-vault/` (clone `81e981d`); used here as a design reference (4.1) |
| ai-memory | Memory shared across agents and machines | |

## 4. Architecture (agreed 2026-11-06)

Goal: many VMs on many clouds, one owner. The stack and the deployables are fixed from the start; features are built
piece by piece (§6).

### Stack

| Layer | Technology |
|---|---|
| Backend (all deployables) | **Go**, modular monolith per deployable in the style of `second-bite/server`: `huma` (OpenAPI 3.1 from code), `pgx`, sqlc, `goose`, River (queue and outbox on Postgres) |
| Database | Postgres (control plane, vault); SQLite journal on each node |
| Brain | **pi** (`earendil-works/pi`) as a child process over **RPC** (`pi --mode rpc`, JSONL commands, responses and events; `docs/rpc.md`: "Other languages, isolated processes"). Tools reach pi over **MCP** served by the control plane (`docs/mcp.md`). Runs on an API key or a cheap model |
| Code workers | Claude Code CLI, Codex CLI, headless with JSON output |
| First channel | Telegram |
| Web (later) | TypeScript, React or Next.js, client generated from the OpenAPI contract |
| Mobile (later) | Flutter, client generated from the same contract (as Northstar and second-bite) |

Why Go for the whole backend: one language; Omnara's Go code (daemon, protocol, sandbox, work claiming) can be read
and reused directly; the `second-bite` layout and its vendored Go skills already exist; `huma` gives one OpenAPI
contract for web and mobile. Trade-off: RPC gives less in-process control than the pi SDK, and the control-plane host
needs Node to run pi; in exchange a pi crash cannot take the control plane down.

### Deployables (three, from day one)

```
Telegram / (later) Web / Mobile
          │ HTTPS
┌─────────▼────────────────────────────┐   create / revoke session   ┌──────────────────────────┐
│ cmd/hypomnema   (control plane)      │ ──────────────────────────▶ │ cmd/hypomnema-vault      │
│ channels · pi supervisor (RPC)       │   never sees secret values   │ key hierarchy · sessions │
│ Run/Task/Dispatch/Gate · event log   │                              │ audit · own VM or host   │
│ MCP bridge · API (OpenAPI)           │                              └────────────▲─────────────┘
└─────────▲────────────────────────────┘                                           │ fetch credential
          │ outbound WebSocket (no inbound ports)                                    │ for a live session
┌─────────┴────────────────────────────────────────────────────────────────────────┴─────────┐
│ cmd/hypomnema-node   on every VM, every cloud, every sandbox (zm, EC2, GCP, Hetzner, K8s,  │
│ Modal, Daytona, E2B…): pairing · worktrees · runs Claude Code / Codex / pi · egress proxy   │
│ beside the worker · process isolation · SQLite journal · self-update                        │
└─────────────────────────────────────────────────────────────────────────────────────────────┘
```

| Deployable | Runs | Owns | Learns from |
|---|---|---|---|
| `cmd/hypomnema` | One place (zm or a VM), public TLS endpoint or Tailscale | Channels, pi supervision, Run → Task (DAG) → Dispatch → Gate, event log, work claiming with leases and a reaper, MCP bridge with policy, node registry and placement, API | Omnara (agent as event log, `FOR UPDATE SKIP LOCKED` claiming, maintenance reaper), Orca (Task vs Dispatch, gates, wake on event, fencing), OpenClaw (runtime layers, `sessions_spawn`, announce, MCP bridge) |
| `cmd/hypomnema-node` | Every machine | Outbound connection and reconnect, pairing, worktrees, CLI execution and parsing, **egress proxy** that attaches credentials, sandbox (landlock, seccomp or container), local journal, self-update | Omnara `internal/machinedaemon`, `internal/daemonprotocol`, `internal/sandbox`, `omnarad` install and update |
| `cmd/hypomnema-vault` | Its own VM or host, separate from the control plane | Key hierarchy, credential sessions per Dispatch, delivery to node proxies, audit, root-key backup | Infisical (key hierarchy, Agent Vault sessions, proxy beside each sandbox), omp (`auth-broker` and `auth-gateway` as separate roles) |

Why the vault is its own deployable: the pi brain runs on the control-plane host and reads untrusted text (messages,
issues). If the control plane is tricked, the root key and the secret values are on another host. The control plane
can only create and revoke sessions; node proxies fetch credentials from the vault for a live session. A separate
process is only a boundary when it also runs under a different identity and host, so the vault gets its own VM.

Even on zm, the first setup runs all three, and the node connects the same way a remote node does. There is no
special local path; adding a machine means installing a node.

### Multi-cloud

- **Node identity**: one per machine, approved once (as OpenClaw pairing), exchanged for short-lived tokens.
- **Placement**: nodes carry labels (`cloud=aws`, `region=sg`, `gpu`, `repo=…`); each Dispatch selects a node by label.
- **CLI logins**: each node logs in to Claude Code and Codex. Track quota per account and cap concurrent workers.
- **Machine pools (later)**: a `machinepool` module creates and destroys VMs and sandboxes through each provider's
  API, as Omnara's pools do.

### Control-plane modules (`internal/`, boundary test as in second-bite)

`identity` (owner, allowlist, web and mobile sessions) · `channel` (Telegram; later Lark, X) · `brain` (pi over RPC,
supervision, restart) · `run` (Run, Task, Dispatch, Gate, event log) · `dispatch` (claiming, leases, reaper) ·
`node` (registry, pairing, placement) · `runtime` (claude-cli, codex, pi adapters: start, parse JSON, stop) ·
`mcpbridge` (tools for pi and workers, with policy) · `integration` (GitHub, MemoryOS, Northstar) · `audit` ·
`platform` (shared technical code).

### Delegation flow

1. A message arrives; only the owner or an allowlist can assign work.
2. The pi brain plans and creates Tasks.
3. **Gate 1**: the owner approves the plan.
4. Each Task becomes a Dispatch placed on a node and runtime; the control plane asks the vault for a credential
   session scoped to that Dispatch.
5. The node runs the worker in a worktree; the worker reaches the network only through the node's proxy, which
   attaches credentials from the vault session. The result is **announced** back to the brain.
6. Review and CI, then **Gate 2**: the owner approves the merge. Closing the Dispatch revokes the session.

**Security**: chat and issue text is untrusted input. A public channel such as X mentions never starts work directly;
it goes through a gate. Workers never hold raw secrets and never get merge rights.

### 4.1 Vault design, learned from Infisical

Source: Infisical docs (clone `81e981d`, sparse checkout of `docs/`): `internals/security.mdx`,
`documentation/platform/agent-vault/how-it-works.mdx`, `documentation/platform/agent-proxy/overview.mdx`,
`documentation/platform/agent-vault/proxies.mdx`.

What Infisical does:
- **Key hierarchy**: an operator root key (256-bit AES, environment variable) encrypts one internal KMS root key, which
  encrypts one data key per organization and per project. All data is encrypted with AES-256-GCM, 96-bit nonces. The
  root key protects only one key, which limits its exposure. External KMS or HSM can seal-wrap the root.
- **Machine identities**: each machine or service logs in (universal auth, cloud, Kubernetes, OIDC…) and gets a
  short-lived token.
- **Agent Vault**: an orchestrator creates a **session** with an **access bundle** (which services and credentials)
  and starts the agent with only the session token. The agent sends every HTTP or MCP request through a **proxy**,
  which runs beside each sandbox, fetches the real credential and attaches it on the way out. The agent never holds
  it, so prompt injection cannot exfiltrate it. Sessions end when the task ends or on expiry. The orchestrator's own
  credentials must stay out of the agent's environment.
- Also: secret versioning, point-in-time recovery, audit logs, dynamic secrets, rotation.

What `hypomnema-vault` takes (first version):
1. The same three-level key hierarchy in its own Postgres, AES-256-GCM from Go's standard library, root key only on
   the vault host, with an offline backup and a rotation procedure.
2. Identities for the control plane (may create and revoke sessions) and for each node proxy (may fetch credentials
   for live sessions on that node only).
3. **Session per Dispatch**: created when the control plane places a Dispatch, revoked when it closes or expires.
4. Every credential use is audited, and the audit is mirrored into the control-plane event log.
5. Later: versioning, rotation, dynamic secrets.

The vault exposes "create session, fetch for session, revoke", so Infisical could replace it behind the same
interface without changing nodes or the control plane.

## 5. Personal OS layer (Northstar)

Northstar (`kl3inIT/northstar`, local `D:\Workspaces\northstar`) is already your personal OS: an assistant and capture,
Markdown notes with hybrid search, projects, tasks and calendar, a VND ledger and budgets, study (FSRS, IELTS), and
typed automations such as the Morning Brief. It is a Spring Boot modular monolith on PostgreSQL and pgvector, with web
and Flutter clients and a **streamable HTTP MCP server** (`/mcp`, header `X-Northstar-MCP-Token`) exposing knowledge,
tasks, calendar, projects, finance, study and reviews.

How it fits Hypomnema:
- **Life data stays in Northstar.** Agents read and write it over MCP; Hypomnema does not copy notes, tasks or the
  ledger.
- **Tasks as one entry point for work**: a Northstar task tagged for an agent can become a Hypomnema task, and the
  result (PR link, research note) is written back to the task or a note. Design open: the trigger (poll, webhook, or
  a Northstar automation).
- **Automations**: personal schedules stay in Northstar automations. Agent work (code, research, operations) runs in
  Hypomnema. One home per job.
- **Memory boundary to decide**: Northstar notes and MemoryOS memory overlap. Proposal: Northstar holds what *you*
  write and own; MemoryOS holds what agents learn while working. Record it in `docs/decisions/`.
- **Secrets**: Northstar's MCP token and AI gateway keys move into the Hypomnema vault and are brokered (4.1).
- Also reuse its repository harness (`harness.md`: repo as system of record, thin `CLAUDE.md`/`AGENTS.md`,
  `ARCHITECTURE.md` for facts only, append-only `docs/decisions/`).

## 6. Build order (one piece at a time; the three deployables exist from step 1)

0. **Spikes** (half a day each): (B) run `claude -p` with JSON output and `codex exec --json` in a worktree, parse
   results, cost and errors, stop a run midway; (A) run `pi --mode rpc` from Go with one MCP tool served by Go.
1. **Skeleton of all three deployables** on zm: `hypomnema` with Telegram, the pi brain and the event log;
   `hypomnema-node` paired over the outbound WebSocket; `hypomnema-vault` with the key hierarchy and sessions. A message
   gets an answer from pi; a Dispatch reaches the node and runs a shell command with a credential from a session.
2. **First PR**: the brain dispatches Claude Code or Codex on the node in a worktree; the worker announces back;
   Gate 1 and Gate 2 are approved from Telegram. Done when one message ends in one merged PR.
3. **Second cloud**: a node on another provider (EC2 or Hetzner) with placement by label; quota tracking and the cap
   on concurrent workers.
4. **Task DAG and stations**: several Tasks per Run, a review worker, the CI fix loop.
5. **Machine pools, web and mobile**: create and destroy VMs and sandboxes on demand; web and Flutter clients from
   the OpenAPI contract.

Earlier research steps: `buzz-acp` read on 2026-11-06 (headless works; engrams are per agent). The Buzz testbed is
dropped: OpenClaw already shows the delegation model (3.3).

## 7. Naming

Criteria: Greek or Latin, niche, about a personal mind, memory, a workshop, or one spirit in many bodies.

| Name | Origin and meaning | Fit | Note (checked 2026-11-06) |
|---|---|---|---|
| **Daimon** | Greek δαίμων: a person's own guardian spirit; Socrates' "inner voice" | One spirit for one person; *daemon* is also a background process on a server; matches "same agent, new body" | npm `daimon` taken; 3 GitHub repos match "daimon agent" |
| **Officina** | Latin: workshop, factory (origin of *office*) | Literally a personal "software factory" | |
| **Ergasterion** | Greek: workshop, place of work | Very niche | Long, hard to type |
| **Hypomnema** ⭐ | Greek: personal notebook of quotes and reflections | Second brain; almost nobody uses it | npm taken; 0 repos match "hypomnema agent" |
| **Oikos** | Greek: the household and estate of one person | An OS for your "digital estate" | npm taken |
| **Lares** | Latin: the household gods of a Roman family | Private, keeps your house | npm taken |
| **Anamnesis** | Greek: recollection (Plato) | Memory without the word "memory" | npm taken |
| **Ariadne** | Greek: gave the thread out of the labyrinth | Guides through data | Crowded, 5 repos |

Avoid: Nous (Nous Research), Hermes (Hermes Agent), Mentor, Kairos, Mneme, Mem, Pi, Jarvis.

**Chosen (2026-11-06): `Hypomnema`**, the name of this repository:
- a personal notebook of what you read and think, which is what the brain layer is;
- the clearest name of the list: 0 GitHub repos match "hypomnema agent", against 3 for Daimon;
- the unscoped npm name is taken, so use a scoped package such as `@<user>/hypomnema`. Domains not checked yet.

Runner-up: **`Daimon`** (one spirit for one person, with the *daemon* pun). It still fits as the name of the node
daemon that runs on each machine.

## 8. Sources

- Omnara: https://github.com/omnara-ai/omnara (docs `machines/`, `concepts.mdx`, `organization/model-providers.mdx`, `self-hosting/architecture.mdx`)
- Buzz: https://github.com/block/buzz (README, `AGENTS.md`, `VISION_REMOTE_AGENTS.md`)
- OpenClaw: https://docs.openclaw.ai/network · https://docs2.openclaw.ai/nodes · https://docs2.openclaw.ai/gateway/pairing.md · https://docs.openclaw.ai/channels/x
- Steinberger: https://newsletter.pragmaticengineer.com/p/the-creator-of-clawd-i-ship-code · https://x.com/steipete/status/2094290652649636173
- Mastra Factory: https://mastra.ai/blog/announcing-mastra-factory-beta · https://mastra.ai/factory · https://mastra.ai/blog/introducing-remote-sandboxes
- OpenFactory: https://github.com/kunalm2345/openfactory · Vercel eve: https://vercel.com/kb/guide/eve-software-factory
- Hermes Agent: https://github.com/NousResearch/hermes-agent · https://hermes-agent.nousresearch.com/docs/user-guide/features/tools
- Meta-harnesses: https://www.augmentcode.com/tools/open-source-agent-orchestrators · https://github.com/omnigent-ai/omnigent · https://github.com/christianreiss/codex-orchestrator · https://superset.sh/
- Lists: https://github.com/andyrewlee/awesome-agent-orchestrators · https://github.com/bradAGI/awesome-cli-coding-agents
- Vault and memory: https://infisical.com/blog/agent-vault-the-open-source-credential-proxy-and-vault-for-agents · https://github.com/akitaonrails/ai-memory · https://themenonlab.blog/blog/mainplane-open-source-distributed-ai-agent-shed
- Infisical: https://github.com/Infisical/infisical (docs `internals/security.mdx`, `documentation/platform/agent-vault/`, `documentation/platform/agent-proxy/`)
- Northstar: https://github.com/kl3inIT/northstar (README, `harness.md`)
- OpenClaw docs (clone `d622a2b`): `concepts/agent-runtimes.md`, `gateway/cli-backends.md`, `specs/codex-supervision.md`, `tools/acp-agents*.md`, `tools/subagents.md`, `maturity/scorecard.md`
- pi: https://github.com/earendil-works/pi (clone `2a32a60`) · oh-my-pi: https://github.com/can1357/oh-my-pi (clone `660a31d`) · Orca: https://github.com/stablyai/orca (`orca skills get orchestration`, v1.4.250)
