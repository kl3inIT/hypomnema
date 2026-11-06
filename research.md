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
| License, size (stars on 2026-11-06) | Apache 2.0, 36.8k stars | Apache 2.0, 3.4k stars | MIT, OpenClaw Foundation; about 394k stars | Open source; license ❓ |

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

### 3.4 Multi-machine orchestration: Orca, OpenClaw, Omnara (read from code, 2026-11-06)

Sources: Orca `.tmp/orca` at `4c13ae46` (sparse: `src/main/runtime/orchestration`, `src/cli/handlers/orchestration`,
`src/shared/remote-runtime`, `src/main/ssh`, `skill-guides/orchestration`); OpenClaw `.tmp/openclaw` at `d622a2b`
(sparse: `docs`, `src/agents/subagents`, `src/agents/harness`, `src/acp`, `src/gateway/worker-environments`,
`src/cli/node*-cli`, `extensions/acpx`, `packages/agent-core`); Omnara `.tmp/omnara` docs (`agents/failure-handling.mdx`,
`machines/overview.mdx`, `agents/configuration.mdx`, `tools/built-in.mdx`) and `internal/storage`. Orca and OpenClaw were
read by research agents; paths are theirs, spot-checked, not every line re-read.

**Three shapes.** Omnara runs the LLM loop centrally and machines only execute commands. Orca has a stateful runtime
(desktop app or `orca serve`) with a thin `orca` CLI over RPC; a coordinator agent drives it, and workers are full
CLI agents in terminals, local or on paired servers. OpenClaw's gateway runs pi; it can place its own pi worker loop
or Codex's remote execution on paired nodes or rented boxes, but Claude Code and ACP sessions are host-only
(`extensions/acpx/src/harness.ts:157`). Hypomnema is Orca's shape (central state, autonomous CLI workers on
machines) with Omnara's durability and OpenClaw's outbound workers.

| Mechanism | Orca | OpenClaw | Omnara | Hypomnema takes |
|---|---|---|---|---|
| State store | SQLite `orchestration.db`, WAL; every lifecycle write is `BEGIN IMMEDIATE` plus a guarded `UPDATE … WHERE state IN (allowed)` that must change one row (`db/lifecycle-transition.ts`) | SQLite by default, Postgres optional (`state.postgres.sql`) | Postgres; agent = event log | Postgres; guarded single-row transitions in a transaction; one canonical lock order (Omnara `lifecyclelock`) |
| Work unit vs attempt | `tasks` vs `dispatch_contexts` (one row per attempt, `retry_of_dispatch_id`, `failure_count`) | `subagent_runs`, `worker_session_placements` | agent step under a lease | Task vs Dispatch (one row per attempt, retry chain, failure count travels only along the chain) |
| DAG | deps as JSON; ready computed in SQL; `promoteReadyTasks` in the completion transaction (`db/tasks/task-store.ts`). **Gap:** failure does not propagate to dependents | none (parent/child tree, depth ≤ 5, 5 children, 8 concurrent per lane) | parent/child tree (`max_subagents`, depth) | DAG as Orca, **plus** failure/cancel propagates to dependents as `blocked` with a reason |
| Retries | circuit breaker after 3 failures → `circuit_broken` | delivery retries until a 30 min deadline | model calls retried 8×; tools not rerun | circuit breaker per Task; interrupted tool or Dispatch is **reconciled, never replayed** |
| Fencing | three mechanisms: process identity (pane + incarnation), `consumer_generation` on mail batches, `runtime_epoch` for restarted runtimes | CAS on `state + transition_generation + environment_id + owner_epoch + claim id/generation` (`placement-turn-claims.worker.ts`) | lease renewed ~30 s; a worker that lost its lease cannot save | one **`epoch`** column on Dispatch plus owner epoch of the node; every write from a node is a CAS on `(dispatch_id, epoch, state)` |
| Unknown outcomes | explicit `start_unknown`, `stop_unknown`; silence never proves exit; a live observation older than 60 s becomes `unverifiable` | operation status `unknown` | "outcome unknown" told to the agent | explicit `*_unknown` states; liveness facts with timestamps and freshness |
| Idempotency | `mutation_receipts (caller_fingerprint, request_id)` + payload hash; `request-show` answers "did it take effect"; `request_mismatch` on reuse with other input | `worker_session_tool_operations (session, claim, tool_call_id)` + `request_digest`; fixed provider lease IDs | dedupe by tool-call ID | mutation receipts as Orca for every control-plane mutation (from pi tools, CLI, Telegram) |
| Mailbox | typed messages (`status, dispatch, worker_done, merge_ready, escalation, handoff, decision_gate, question, heartbeat`), ordered by `sequence`; a `check` returns the same unacknowledged batch until `--ack`; groups resolved at send time | delivery record per child with generation-scoped key, separate from the run record | result arrives as a message in the parent's timeline | typed messages in Postgres, batch + ack, stable content-derived IDs (escalations deduped); delivery record separate from the Dispatch |
| Waking the coordinator | resolves an in-memory waiter, else types a pointer into the idle terminal | steer into the active turn or queue in the session lane | worker claims the agent when input arrives | brain is woken by an event (River job) — no polling, no terminal typing |
| Worker reports done | `worker_done {taskId, dispatchId, outcome}`; authority from the observed sender, not the body; duplicates return `duplicate:true` | announce path | final answer as message | node reports with `(dispatch_id, epoch)`; authority from the node identity bound to the Dispatch |
| Worker stops silently | escalation to the coordinator, never auto-settle | 60 s "no output" notice | machine unreachable after 30 s | escalation to the brain and to Telegram; the owner or brain decides |
| Gates | `decision_gates`; opening blocks the task; only explicit resolution unblocks. **Gap:** `timeoutGate` has no caller | approvals per call | approvals wait in Postgres indefinitely | Gate as Orca **with a timeout** and a reminder |
| Remote connection | home dials the server over WebSocket, E2E-encrypted, pinned Curve25519 key from the pairing code; SSH deploys `orcad` on loopback via an SSH stdio bridge (`src/main/ssh/orcad-stdio-bridge.ts`) | **node dials the gateway** (TLS WebSocket), two approvals (device, then commands), Ed25519 identity | `omnarad` dials out | node dials the control plane (as OpenClaw, Omnara); pairing with a pinned key (as Orca); SSH executor bootstraps the node over an SSH stdio bridge instead of opening ports |
| Remote sync | per-Dispatch outbox with per-direction sequence → pull after sequence → contiguity check → idempotent import → ack-through; cursors reset on worker-host `runtimeEpoch` (`federation-sync.ts`); ping 10 s, dead after 25 s | transcript commits via `(session_id, run_epoch, next_seq)`; ACK cursors only move forward | — | the same outbox/sequence/ack/epoch protocol, **direction flipped**: the node pushes its outbox and long-polls its inbox |
| Credentials | device token in the pairing code | minted per dispatch, stored hashed, bound to the claim (`credential.ts`) | `secret_env` from scoped secrets | vault session per Dispatch (4.1), bound to node identity and epoch |
| Tool location | — | workspace/process tools run remotely, authority-bearing tools stay on the control plane | machines run commands only | same split: GitHub merge, vault, gates stay on the control plane; files and processes on the node |
| Workspace results | — | staged under `refs/openclaw/worker-results/` before the claim is released | — | push to a branch named for the Dispatch before reporting done; the ref is the recovery fence |

**Not copied:** Orca's terminal panes, PTY pointer typing and hook-state stop detection (desktop app); embedded
single-writer SQLite on the control plane; OpenClaw's single-gateway assumptions (children die on restart, in-memory
standing approvals, host-only CLI runtimes).

**Not yet read** (add to the sparse checkouts when designing the node): OpenClaw `src/gateway/`, `src/node-host/`,
`src/worker/`, `src/agents/cli-runner/` (MCP bridge), `src/infra/`, `packages/gateway-protocol/`; Orca has no
`--host ssh:` flag in this revision, only the `orcad` SSH bridge.

### 3.5 Harness drivers and durability: Omnigent, genie, durable-harness, companion (read from code, 2026-11-06)

Sources (in `.tmp/`, read by research agents; paths are theirs): Omnigent `7230afa` (Apache-2.0, Python, sparse
`omnigent/`, `docs/`, `specs/`); genie `68bf387` (MIT); durable-harness `c7d2472` (Apache-2.0); companion `c950f6f`
(MIT). Who uses pi as the brain: only OpenClaw at scale (3.3, 3.4); omp is a single-machine fork; companion runs pi
durably in a container; genie uses pi only for small schema-bound "mikro" agents; Omnigent and durable-harness treat pi
as one harness among many.

**Omnigent** (meta-harness over Claude Code, Codex, Cursor, Pi, Hermes…):
- Processes: server (FastAPI, Postgres/SQLite/CockroachDB via Alembic), **host daemon** on each machine (outbound
  WebSocket, control frames only: launch runner, worktree, secrets, harness login; its hello lists live runners for
  reconciliation, `omnigent/host/connect.py`), and one **runner** per session with its own outbound tunnel
  (`omnigent/runner/transports/ws_tunnel/serve.py`). No inbound listeners anywhere.
- One interface, `Executor.run_turn(...) -> AsyncIterator[ExecutorEvent]` (`omnigent/inner/executor.py`); each harness
  declares a **capability matrix** (integration mode, elicitation style, resume, auth; `omnigent/harness_capabilities.py`).
- Headless drivers: Claude Agent SDK with tools as MCP; one long-lived **`codex app-server`** per session over
  JSON-RPC; **`pi --mode rpc`** with tools from a generated extension, turn ends at `agent_settled` (Pi ≥ 0.80.4,
  `omnigent/inner/pi_executor.py`). "Native" variants run the real TUI in tmux with `send-keys`, and use Claude Code
  PreToolUse/UserPromptSubmit hooks for policy.
- Policies ALLOW / DENY / ASK at session → agent → server, fail-closed; `spawn_bounds` (≤ 5 dispatches per turn),
  `worktree_guard`, `blast_radius`; MITM egress proxy with default-deny rules; **credential proxy**: the sandbox holds
  only `oa_cred_*` placeholders and the proxy swaps in the secret per host (`omnigent/inner/credential_proxy.py`).
- Managed hosts in Modal, E2B, K8s with a per-launch token; a replacement sandbox keeps the same `host_id`.
- Resilience contract tested with a fault-proxy lab (`docs/network-resilience.md`). Their own gaps: pending approvals in
  server memory, no event cursor (out-of-order replay), no task/DAG model ("turn/steering state … in-memory in the
  runner"). PRs are discovered from tool output and reported until acknowledged.

**genie** ("wishes in, PRs out"):
- One task runs as a scripted chain of schema-bound agents: Admit → Work → Gate → Review → Repair → Publish →
  Read-back (`.claude/workflows/wish.js`).
- Admission limits (≤ 25 files, ≤ 2,000 insertions) and a **denylist** of trust-boundary paths (`.github/`, hooks,
  `Makefile`, package scripts) that forces a written plan; plans are `WISH.md` with groups, owned files, `depends-on`
  and a validation command, approved before work.
- Parallel writers need disjoint files or their own worktrees; worktree path from `git rev-parse --git-common-dir`.
- Review by a **different, read-only agent on the exact commit SHA** (SHIP / FIX-FIRST / BLOCKED); repair budget 2;
  **read-back in code, no agent**: remote head = local head, PR files = declared files, verdict SHIP; never merges, the
  human does.
- Security note: its worker template launches Claude with `--dangerously-skip-permissions`.

**durable-harness** (Vercel Labs):
- Every action goes through `ctx.effect(key, {replay: 'safe' | 'never'}, fn)`: commit intent, run, commit outcome; a
  `never` action interrupted by a crash returns "interrupted" with its partial output (`harness-world/src/types.ts`).
- After a crash the agent gets a **recovery note** listing finished and interrupted calls ("may have taken effect:
  check before repeating").
- Claude Code through the Agent SDK `query()` with a `SessionStore` mirror into the journal; Codex through
  `codex app-server --stdio` (`thread/start|resume`, `turn/start|steer|interrupt`); model calls through a gateway so the
  VM never holds a credential; one process group per session with kill-before-resume.
- Pi via pi-durable with a fenced single-owner `Storage` (`StorageFenced`). Not available through `pi --mode rpc`.

**companion** (durable pi companions in a Box):
- Daemon in the container: idempotent `PUT /runs/:id` with a request hash (`IDEMPOTENCY_CONFLICT` on mismatch);
  on restart every unfinished run becomes `interrupted` and is **never redispatched**.
- Steering is journaled before pi sees it; cancel aborts the controller and the pi session.
- Questions are rows (`task_questions`) answered out of band, capped at 2 hours.
- Single executor leader by `pg_try_advisory_lock`, writes guarded by the lock holder; UI updates via
  `pg_notify` → SSE.

**What Hypomnema takes from 3.5** (adds to 3.4):

| Pattern | From | Where in Hypomnema |
|---|---|---|
| Outbound control channel per node (hello lists live workers) + one data stream per worker | Omnigent host/runner | `hypomnema-node` protocol |
| Capability matrix per runtime (mode, questions, resume, auth) | Omnigent | `runtime` registry (Strategy behind a registry) |
| Headless drivers: `codex app-server` JSON-RPC, Claude Agent SDK or stream-json, `pi --mode rpc` ending at `agent_settled` | Omnigent, durable-harness | `runtime` adapters; spike #1 checks the Claude path |
| Replay policy `safe` / `never`, intent before effect, recovery note instead of replay | durable-harness, companion | every Dispatch effect (push, PR, merge request) on the node and control plane |
| Idempotent run start with request hash; restart marks runs `interrupted`, never redispatches | companion | node daemon run API |
| Placeholder credentials swapped by the proxy | Omnigent, durable-harness | node egress proxy + vault session (4.1) |
| ALLOW / DENY / ASK, fail-closed, spawn bounds, blast radius, egress default-deny | Omnigent | `mcpbridge` and node proxy policy |
| Admission limits and denylist; reviewer ≠ author on the exact SHA; repair budget; read-back in code; human merges | genie | the PR flow in step 2 |
| Questions as rows answered out of band | companion | Gate rows answered from Telegram, with timeout |
| Advisory-lock leader + `LISTEN/NOTIFY` | companion | control-plane background loops (or River) |
| Resilience contract and fault lab | Omnigent | acceptance tests for node and control plane |

Not taken: tmux keystroke injection (fragile), approvals in memory, no event cursor, Vercel Workflow World as a
dependency (copy its semantics into Postgres instead), `--dangerously-skip-permissions` without a sandbox.

### 3.6 pi as brain: RPC vs SDK vs fork, extensions, chat UI (read from code, 2026-11-06)

Sources: pi `.tmp/pi` at `2a32a60` (`packages/coding-agent/docs/rpc.md`, `sdk.md`, `extensions.md`,
`rpc-extension-ui.md`); OpenClaw `.tmp/openclaw` at `d622a2b` (`docs/agent-runtime-architecture.md`,
`docs/concepts/agent-runtimes.md`, `docs/web/index.md`, `docs/web/webchat.md`, `docs/web/tui.md`,
`packages/agent-core/src/`) and its pull requests on GitHub; Orca `.tmp/orca` at `4c13ae46`
(`skill-guides/orchestration/references/coordinator-loop.md`).

**SDK or RPC.** pi documents two integration interfaces (`rpc.md`):

| Interface | Process boundary | Best fit |
|---|---|---|
| SDK (`@earendil-works/pi-coding-agent`, `createAgentSession()`) | In process, Node.js or Bun | TypeScript hosts that want the full API |
| RPC (`pi --mode rpc`) | Child process, JSONL on stdin/stdout | Other languages, isolated processes, IDEs, custom clients |

The control plane is Go, so the SDK would need a Node sidecar that re-implements RPC. RPC is the documented path.
In RPC mode pi draws no UI: the client renders events, and extension dialogs travel as `extension_ui_request` /
`extension_ui_response` records.

**How OpenClaw went from pi to its own runtime.** The history, from its pull requests:

| When | How OpenClaw used pi | Evidence |
|---|---|---|
| 2025-12 | Child process over RPC (a `tau-rpc` module) | openclaw/openclaw#25 |
| 2026-01 to 2026-05 | Embedded SDK: `@mariozechner/pi-agent-core`, `pi-coding-agent`, `pi-tui`, a `pi-embedded-runner` | #4525, #10757, #67813 (bump to 0.67.5) |
| 2026-05-27 | Pi-derived code moved into the repo as `@openclaw/agent-core` | #85341 "refactor: internalize OpenClaw agent runtime" (steipete, merged) |
| 2026-06 on | Own fixes and extensions to the fork | many `fix(agent-core): …` PRs (#89061, #94412, #98097) |

- #85341 extracts `@openclaw/agent-core` "from the previously embedded agent runtime code", renames `pi-tools*`,
  `pi-hooks`, `pi-settings` to `agent-*`, and updates license text "for the incorporated Pi-derived code". The core
  is still pi's: `class Agent`, `runAgentLoop`, `runAgentLoopContinue` exist in both `pi/packages/agent/src` and
  `openclaw/packages/agent-core/src`, and OpenClaw adds steering, tool-batch admission, turn taint, repetition guard.
- Their reason ("Why" in #85341) is ownership, not quality: pi names in packages and types, provider fallbacks kept
  only for the dependency boundary, parallel session and transcript stacks, provider dependencies in root, docs
  teaching a pi-era architecture. Goal: "make the structure look like Pi had never existed as a separate internal
  dependency".
- The cost of the SDK period: #55867 (embedded auth broken by the `pi-coding-agent` 0.63.0 bump), #6970
  (`ToolDefinition.execute` signature changed in 0.51.0), #71875 (SDK overrode OpenRouter attribution), #34620 (pi
  auto-retry disabled so failover works).
- `@earendil-works/pi-tui` is still a dependency (local TUI and tool renderers).
- Not verified: whether `tau-rpc` was the old name of pi's RPC mode (the clone has one commit, no history).

**Brain shape: runtime or coordinator.**

| | How | Fits Hypomnema? |
|---|---|---|
| OpenClaw | pi-derived loop runs in the Gateway process as the default runtime; Codex, Copilot and `claude-cli` are added runtimes | No: same language as pi and a team to maintain a fork |
| Orca | Coordinator is a CLI agent issuing `orca orchestration run-create / task-create / worker-start`; Run/Task/Dispatch live in Orca's store; workers are full CLI agents in their own worktrees | Yes: the brain holds no state, so it can crash or be replaced |
| Omnara | LLM loop central, machines only run commands | Central state yes; workers here are autonomous CLIs |

**Decision (2026-11-06):** pi is the **coordinator**, in Orca's shape, over `pi --mode rpc`; it is **not forked**.
Plans, Tasks, Gates live in Postgres; code work goes to Claude Code and Codex; pi can also be a worker runtime for
small cheap jobs (the `runtime` module already lists a pi adapter). Upgrading pi means re-checking the RPC protocol
(commands, events, `agent_settled`), not an internal TypeScript API. Revisit forking only if a change inside the loop
cannot be made with an extension or MCP.

**Extensions still work in RPC mode** (`extensions.md`). They run inside the pi process, so the loop can be changed
without a fork:

| Need | Hook or API |
|---|---|
| Add a tool | `pi.registerTool()` |
| Block or rewrite a tool call before it runs | `tool_call` (mutate `event.input`, or return `{block, reason, terminate}`) |
| Change a tool result | `tool_result` |
| Change the context before each model call | `context`, `context_with_system` |
| Change the prompt or tool set per run | `before_agent_start`, `pi.setActiveTools()` |
| Provider request and response | `before_provider_request`, `before_provider_headers`, `after_provider_response` |
| Make the agent continue before it settles | `agent_before_settle` (add entries, `continue: true`) |
| Own compaction | `session_before_compact` |
| Filter input | `input` (`event.source` is `"rpc"` for RPC prompts) |
| Providers, virtual models, MCP servers, commands, durable state | `registerProvider`, `registerCommand`, `pi.appendEntry()` |

Limits (`rpc-extension-ui.md`): `ctx.ui.custom()` returns `undefined`; header, footer, editor and theme calls are
no-ops. `select`, `confirm`, `input`, `editor`, `notify` work through the extension UI records, so Go can turn them into
Gates or Telegram/web questions. Extensions are TypeScript with the full permissions of the pi process ("load
extensions only from sources you trust").

Rule for Hypomnema: keep the extension **thin**, a hook adapter (as Omnigent generates one to expose its tools to pi).
Every policy decision (ALLOW / DENY / ASK, spawn bounds, blast radius) is made in Go behind `mcpbridge`, so pi and the
Claude Code and Codex workers share one policy. This replaces the earlier note that RPC cannot guard inside the loop.

**Chat UI.** OpenClaw has a browser Control UI (Vite + Lit, served from the Gateway port), native macOS/iOS WebChat,
`openclaw tui` (built on `pi-tui`, can attach to a remote Gateway with `--url … --token …`) and `openclaw chat` (local,
no Gateway). Every client uses the same Gateway WebSocket RPC (`chat.startup`, `chat.history`, `chat.send`,
`chat.message.get`); none talks to the runtime directly. Patterns from `docs/web/webchat.md` worth copying:
- `chat.send` carries a dedupe key (the run id); retries never start a second run.
- History pages by bytes with a `deltaCursor`; a stale cursor returns `{kind: "reset"}` and the client reloads the tail.
- Subscribe to session events before reading history, so nothing arriving in between is lost.
- Messages with an uncertain delivery show "Waiting for reconnect"; messages queued on the Gateway show above the
  composer.
- `expectedLeafEntryId` parks a message instead of posting it to a branch another client switched to.

For Hypomnema (proposal; build order unchanged until decided): a web chat first, TUI later. Clients talk to the
control plane, never to pi: `POST` messages over the OpenAPI contract, events over SSE from the Postgres event log with
`Last-Event-ID`, plus OpenClaw's dedupe key, reset cursor and subscribe-before-read. pi events are mapped to one
Hypomnema event type shared with Claude Code and Codex workers. Gates and questions are rows, answerable from web or
Telegram. A TUI, if wanted, is one more client of the same API (Bubble Tea in Go). OpenClaw uses WebSocket RPC
instead; HTTP + SSE is chosen here because huma generates one OpenAPI contract for web and Flutter.

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
needs Node to run pi; in exchange a pi crash cannot take the control plane down. Hooks inside the loop still come from
a thin pi extension (3.6).

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
- OpenClaw pull requests: https://github.com/openclaw/openclaw/pull/85341 (internalize the runtime) · #25 · #4525 · #6970 · #10757 · #34620 · #55867 · #67813 · issue #71875
- pi: https://github.com/earendil-works/pi (clone `2a32a60`) · oh-my-pi: https://github.com/can1357/oh-my-pi (clone `660a31d`) · Orca: https://github.com/stablyai/orca (`orca skills get orchestration`, v1.4.250)
