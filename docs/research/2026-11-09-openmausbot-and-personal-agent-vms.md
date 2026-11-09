# OpenMausBot, Muse and Grok Bot

Date: 2026-11-09. OpenMausBot was read from its clone in `.tmp/openmausbot` (commit `1de1164`, Apache 2.0); paths are
relative to that clone. Muse and Grok Bot are closed products: what is said about them comes from public articles
(sources at the end), not from code. Inferences are marked.

The question: OpenMausBot, Meta's Muse and xAI's Grok Bot give each agent "its own computer". Do they drive the coding
CLIs as Hypomnema does, how do they differ from Hypomnema, and what is worth taking from them?

## 1. What each one is

| | Muse (Meta) | Grok Bot (xAI) | OpenMausBot | Hypomnema |
|---|---|---|---|---|
| Kind | Personal assistant: browses, buys, uses the person's accounts | Bots as teammates with shared tools | A desktop chat app (Electron) where each contact is a bot | A software factory: a task becomes a merged PR |
| Where the agent runs | A dedicated cloud VM per user (Muse Secure VM) | A persistent cloud computer; articles disagree on one per user or one per bot | On the person's machine: one harness server on `127.0.0.1` owns every agent process (`README.md`) | On nodes the owner runs; the control plane is on a server, so a laptop can be off |
| Model | The vendor's | The vendor's | The CLIs the person already has, on their own logins and subscriptions | The same |
| The agent's "computer" | The VM | The cloud computer | A Linux desktop the agent controls (computer use): a cloud desktop, a local VM, a Docker container on the person's VPS, or the host itself. On a VPS "the agent process stays on your machine" and it "never runs an agent remotely" (`docs/byo-vps.md`) | The node is where the worker runs and works on a git worktree; no desktop |
| Source | Closed | Closed | Open | Open |

So "its own computer" means two different things. For Muse and Grok Bot the agent itself lives on the vendor's VM.
For OpenMausBot the agent lives on the person's machine and the remote computer is only a screen it drives. Hypomnema
puts the agent itself on the owner's machines, which is closest to Muse's shape but on the owner's hardware and
subscriptions.

## 2. How OpenMausBot drives the CLIs

Yes, it drives the CLIs, as Hypomnema does, plus API drivers for models without a CLI.

| Runtime | How (`server/drivers/`) | Compared with Hypomnema |
|---|---|---|
| Claude Code | `claude.ts`: stream-JSON both ways, prompt on stdin, **one retained CLI process per thread**, each turn settled by the CLI's `result` frame; `--resume` restores a session | The same protocol and flags as `internal/node/claude/runner.go` (spike B). Differences: permission prompts go to an MCP tool (`--permission-prompt-tool mcp__ogb__approve`, `claude.ts` L1131) where Hypomnema uses `stdio`; and the process is kept between turns (below) |
| Codex | `codex.ts`: `codex app-server` JSON-RPC over stdio; completion is `turn/completed`; approvals arrive as server-to-client requests; the resume cursor is Codex's thread id | The integration chosen in spike B |
| pi | `pi.ts`: `pi --mode rpc --no-session`, its own JSON-RPC, not ACP; resume by `sessionFile` | The mode spike A used |
| Grok, Gemini, Cursor, OpenCode, Hermes, Kimi, Qwen, Copilot, Droid… | ACP, one shared core (`drivers/acp/core.ts`) and one file per agent (`drivers/acp/grok.ts`, `hermes.ts`…) | Hypomnema has no ACP runtime yet |
| Models without a CLI | OpenAI-compatible chat drivers (`grok.ts` for the xAI API, `openrouter-free.ts`, `ollama-context.ts`…) | Out of scope: Hypomnema's workers are CLIs |

Mechanics around the drivers that Hypomnema has not built:

- **Warm processes, given back when idle.** One warm engine with its MCP servers takes 250–450 MB. An idle process is
  stopped after 10 minutes (150 s on a host under 1.5 GB), and the next turn resumes the session by id
  (`drivers/session-idle.ts` L10-15).
- **Launch slots for Codex.** Each Codex app-server starts its MCP servers with its thread; many at once push those
  calls past their timeouts on a small host, so launches beyond a limit wait (`drivers/codex-process.ts`).
- **Plan limits from the CLI's own frames.** Claude's `rate_limit_event` and `rate_limit_info` give the window
  (`five_hour`, `seven_day`, `seven_day_opus`…), its status (`allowed`, `allowed_warning`, `rejected`) and the reset
  time (`drivers/claude-limits.ts`, `claude.ts` L1582). Hypomnema cools an account down on a rate limit; these frames
  could give the exact reset time (inference: not checked against Hypomnema's runner).
- **Prompt-cache keep-warm.** After an idle stretch the driver sends one tool-less request forked from the live
  session (`--resume <session> --fork-session --no-session-persistence`, every tool denied by a hook) so the cached
  prefix stays alive (`drivers/claude-keepwarm.ts`, ported from Cache Tax).
- **Much of the harness is ported from T3 Code** (`NOTICE`, `third_party/t3-code/`, `server/harness/registry.ts`).

## 3. The bot box: a sandbox without root

On its cloud hosts every bot turn runs inside one unprivileged bubblewrap sandbox for the process's lifetime: the
engine, its shell commands, its MCP servers and the helper proxies (`docs/bot-box.md`, code in `server/bot-box/`).

- Read-only: `/usr`, `/etc`, `/opt` and the server's code. Read-write: HOME (the engine's own logins), the turn's
  working folder and the bot's own folder. The data folder is an empty private folder: the database, settings and
  other bots' folders are not there.
- Its own PID namespace, private `/tmp`, and a network namespace with loopback only. Two loopback ports are bridged
  to unix sockets: the permission socket and an egress gate. There is no other route out.
- It runs as the server's own user inside a user namespace. When the kernel refuses user namespaces or `bwrap` is
  missing, the box reports `off` with the reason (L52, L71).
- A separate Linux user per bot was not built: it needs a privileged helper (L118).

Hypomnema's first runs left sandboxing out because Claude's own sandbox needs root on the host (issue #32, the first
end-to-end run). Bubblewrap with user namespaces needs no root, so it may close that gap (inference: needs a spike on
the node's kernel and distribution).

## 4. The runtime contract: one record for all delegated work

`docs/runtime-contract.md` replaced "two delegation systems, six wake lanes, a dozen clocks and ten trust wordings"
with one record, `Assignment` (`server/runtime/types.ts`), and one lifecycle.

- **A tree:** `rootId`, `parentId`, `depth`. Enqueue refuses work handed back to an ancestor, dedupes the same brief to
  the same assignee under the same parent, and checks depth and the root's budgets.
- **Origin written once at the root and copied down:** `person`, `routine` or `outside` (webhook, relay, guest, peer,
  API). An outside event's brief "is not their authority": its questions still go to the person.
- **Mode and reply:** `work`, `consult` (answer, act on nothing) or `notify` (one-way); the requester is woken
  (`resume`), reads an inbox (`poll`) or expects nothing (`none`).
- **Lifecycle:** `queued → running → completed | failed | cancelled`, with `waiting` (children running), `resume` and
  `needs_input`. A child's question interrupts the parent's wait. One wake per result, on outcomes and questions,
  never on progress.
- **Stall detection across continuations:** a turn that stops short ends `continue` and is queued again; two
  continuations in a row with no successful tool call, or the same error three times, end it `failed(stalled)`.
- **Cancel cascades** down the tree and is itself a reported outcome.

Hypomnema's Run, Task and Dispatch already cover the single-level case with leases and fencing that OpenMausBot does
not have. The parts that matter for the coordinator are the origin carried from the root, the `consult` and `notify`
modes, and the progress rule for continuations (inference).

## 5. Smaller things

- **Approval levels** (`docs/approval-levels.md`): each level is the provider's own permission mode, passed through.
  OpenMausBot answers a prompt only from an explicit grant (full access, a saved exact command, a read-only MCP tool,
  a bot reading its own folders). The level a delegated turn inherits is capped by what the assignee's engine
  supports.
- **Phone web app** (`docs/phone-web-app.md`): the web UI installs to the home screen as a PWA, reached over the
  workspace's HTTPS address (hosted, Tailscale HTTPS or a reverse proxy) and paired per device; no offline chat or
  push.
- **Sentinel** (`docs/sentinel.md`): a separate process, no model calls, that opens the databases read-only, groups
  failures into issues, writes them outside the data folder and keeps working after the app crashes.
- **Bring your own VPS** (`docs/byo-vps.md`): Docker's SSH transport (`docker -H ssh://`) reaches the VPS; SSH is the
  only credential and no public port is opened.

## 6. What applies to Hypomnema

All of this is inference, for later issues; nothing is decided here.

1. Spike bubblewrap on the first node as the worker's sandbox (section 3). Highest value: it addresses what issue #32
   left out.
2. Read the reset time from Claude's `rate_limit_event` for account cooldowns (section 2).
3. Take origin-at-the-root, `consult`/`notify` and the continuation progress rule into the coordinator's design
   (section 4).
4. Keep one worker process per Conversation between Dispatches only if start-up cost shows up in measurements; today
   each Dispatch resumes the session, which is the state OpenMausBot falls back to after its idle timeout.

## Sources

- OpenMausBot: https://github.com/milind-soni/OpenMausBot
- Muse: https://about.fb.com/news/2026/09/introducing-muse
- Grok Bot and Muse compared:
  https://forkast.news/same-blueprint-different-locks-grokbot-and-muse-ship-the-persistent-agent-vm/ ·
  https://aiidelist.com/blog/grok-bot-vs-muse-vs-dots
- Grok Bot: https://ai-solutions.daviesmeyer.com/en/blog/grok-bot-ki-teammates-xai
