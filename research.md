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
| Harness | **ACP → Claude Code, Codex, goose, OpenCode** | Its own (Go worker) | Its own | Its own |
| Subscription | ⚠️ `buzz-acp` README documents `OPENAI_API_KEY` ("not a ChatGPT subscription") and `ANTHROPIC_API_KEY`; subscription login not documented | ❌ API key only; can point at a proxy such as 9Router (watch the terms of service) | ❓ | ❓ |
| Many servers | ⚠️ `buzz-acp` is a headless daemon that connects to a relay over WebSocket, so it runs on any server; managed remote bodies are 🚧 (`buzz-backend-kubernetes`, mesh) | ✅ BYO machines (`omnarad`, outbound connection, no open ports) and pools of 12 sandbox providers | ✅ Gateway and nodes (pairing approval, SSH or Tailscale) | ⚠️ One SSH target per configuration |
| Shared memory | ⚠️ engrams (NIP-AE) are per agent–owner pair, encrypted to the owner; **not shared between agents** | ✅ memory stores `/memory/<store>/…`, files without history | ✅ at the Gateway | Local files (`MEMORY.md`); Honcho plugin |
| Vault | ⚠️ keypair per agent, secrets kept out of configuration | ✅ secrets scoped to org, project or user, `secret_env` | ❓ | `.env` per instance |
| Chat channels | The workspace itself (desktop; mobile in progress) | Slack, Discord, GitHub, custom | Telegram, X and many more | Telegram, Discord… |
| License, size (stars on 2026-11-06) | Apache 2.0, 36.8k stars | Apache 2.0, 3.4k stars | Open source, run by a foundation; license ❓ | Open source; license ❓ |

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

## 4. Recommended architecture

```
              Telegram / Lark / X / Buzz room
                          │
              ┌───────────▼──────────────────┐
              │ Brain: relay / control plane │  shared memory, history, coordination
              └───┬──────────┬───────────────┘
       knowledge  │          │ secrets
   (MemoryOS, MCP)│          │ (Hypomnema vault)
        ┌─────────┼──────────┼─────────┐
       zm      server B   sandbox    laptop    ← each machine runs a node daemon + Claude Code / Codex
```

**Decision (2026-11-06): build the core yourself, adopt the hands.** The parts that hold your identity, secrets,
memory and decisions are built in Hypomnema, learning from the projects above, so no outside product owns them. The
parts that only execute (the vendor coding CLIs, their ACP adapters, sandbox providers) are reused as they are.

| Layer | Build or adopt | Learn from |
|---|---|---|
| Control plane: event log, agents, tasks, approvals | **Build** | Buzz (signed event log, rooms per branch, author gate), Omnara ("the machine does not own the agent", agent as event log) |
| Node daemon on each machine | **Build** | Omnara `omnarad` (outbound connection only, self-update, systemd or launchd), OpenClaw node pairing approval |
| Vault and credential broker | **Build** (4.1) | Infisical key hierarchy, machine identities, Agent Vault sessions and proxy |
| Shared memory | **Have**: MemoryOS over MCP | Buzz engrams for per-agent private notes |
| Personal OS: notes, tasks, calendar, finance | **Have**: Northstar over MCP (§5) | |
| Coding harness | **Adopt**: Claude Code, Codex via `claude-agent-acp`, `codex-acp` | `buzz-acp` for how to drive them over ACP |
| Sandboxes | **Adopt**: Daytona, E2B, Modal… when needed | Omnara pools |

1. **Interim runway**: until the control plane exists, a self-hosted Buzz relay plus `buzz-acp` on each server is a
   working testbed (headless, one keypair per agent). Use it to learn what the control plane must do, not as the
   base.
2. **Cost**: Buzz documents API keys only. If the node daemon drives the CLIs on a subscription, check the vendor
   terms first (3.1).
3. **Knowledge**: MemoryOS through MCP is the **shared** memory; per-agent notes stay private to each agent.
4. **Security**: issue and chat text is untrusted input while the agent holds tokens.
   - Only the owner (or an allowlist) can assign work, as in Buzz's `--respond-to owner-only`.
   - A public channel such as X mentions never starts work directly; it goes through an approval step.
   - Agents get brokered credentials (4.1), never raw secrets. Merge rights stay behind a human or CI gate.
5. **Record each decision**: one file per decision in `docs/decisions/`, append-only, in the style of Northstar's
   `harness.md`.

**Is it overkill?** Building the hands would be. Building the brain (control plane, vault) is the point of the
project. Keep each piece small: one Postgres, one daemon, one MCP surface.

### 4.1 Vault design, learned from Infisical

Source: Infisical docs (clone `81e981d`, sparse checkout of `docs/`): `internals/security.mdx`,
`documentation/platform/agent-vault/how-it-works.mdx`, `documentation/platform/agent-proxy/overview.mdx`.

What Infisical does:
- **Key hierarchy**: an operator root key (256-bit AES, environment variable) encrypts one internal KMS root key, which
  encrypts one data key per organization and per project. All data is encrypted with AES-256-GCM, 96-bit nonces. The
  root key protects only one key, which limits its exposure. External KMS or HSM can seal-wrap the root.
- **Machine identities**: each machine or service logs in (universal auth, cloud, Kubernetes, OIDC…) and gets a
  short-lived token.
- **Agent Vault**: an orchestrator creates a **session** with an **access bundle** (which services and credentials)
  and starts the agent with only the session token. The agent sends every HTTP or MCP request through a **proxy**,
  which attaches the real credential on the way out. The agent never holds it, so prompt injection cannot exfiltrate
  it. Sessions end when the task ends or on expiry. The orchestrator's own credentials must stay out of the agent's
  environment.
- Also: secret versioning, point-in-time recovery, audit logs, dynamic secrets, rotation.

What Hypomnema's vault takes (first version):
1. The same three-level key hierarchy in Postgres, AES-256-GCM, root key in an environment variable on the control
   plane only.
2. One identity per node daemon, approved once (as in OpenClaw pairing), exchanging for short-lived tokens.
3. **Session per task**: the control plane creates the session when it assigns a task; the node daemon runs the
   proxy beside the agent; the session is revoked when the task closes. This is the main reason to build it in, since
   the control plane already knows the task.
4. Audit every credential use into the control-plane event log.
5. Later: versioning, rotation, dynamic secrets. Not needed for one user at first.

Infisical stays an option to integrate behind the same interface (the vault exposes "create session, proxy request,
revoke"), so the choice can be revisited without changing agents.

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

## 6. Next steps (in order)

1. ~~Read `buzz-acp`: headless, and how engrams work.~~ Done 2026-11-06: headless works (see 3.3); engrams are per
   agent, so shared memory comes from MemoryOS. Still open: whether `claude-agent-acp` and `codex-acp` accept a
   subscription login on a server, and whether the terms allow it.
2. Testbed: self-host a Buzz relay on zm, add one Claude Code agent and one Codex agent with MemoryOS and Northstar
   MCP, run one task that ends in a PR. Write down what the control plane must do.
3. Write the first decisions in `docs/decisions/`: build the core, memory boundary (Northstar vs MemoryOS), vault
   design (4.1).
4. Build the vault first (key hierarchy, node identities, session per task, proxy), since every later piece needs it.
5. Build the control plane and node daemon; replace the Buzz testbed.
6. Read Paseo, Omnigent and Codex Orchestrator for how they drive the CLIs on many servers.

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
