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
| Where the agent runs | A dedicated cloud VM per user (Muse Secure VM) | A persistent cloud computer; articles disagree on one per user or one per bot | Where its harness runs, its "home": the person's machine (one harness server on `127.0.0.1` owns every agent process, `README.md`), or an OMB Cloud home (one always-on Fly machine per customer, `docs/cloud-pro.md`) | On nodes the owner runs; the control plane is on a server, so a laptop can be off |
| Model | The vendor's | The vendor's | The CLIs the person already has, on their own logins and subscriptions | The same |
| The agent's "computer" | The VM | The cloud computer | A Linux desktop the agent controls: a Boat (boat.dev cloud sandbox), a local VM, a Docker container on the person's VPS, or the host itself. The agent reaches it only through MCP tools the harness serves (`screenshot`, `click`, `type_text`, `scroll`, `open_url`, `copy_to_computer`, `copy_from_computer`, and `exec` for shell commands), and "the Boat credential never leaves this server" (`server/cloud-computer-tools.ts`). On a VPS it "never runs an agent remotely" (`docs/byo-vps.md`) | The node is where the worker runs and works on a git worktree; no desktop |
| Source | Closed | Closed | Open | Open |

So "its own computer" means two different things. For Muse and Grok Bot the agent itself lives on the vendor's VM.
For OpenMausBot the agent lives in its home (the person's machine, or a Cloud home) and the computer is a pair of
hands it drives through tools: a screen and a shell. On the person's own machine nothing runs while it sleeps
(`docs/routine-schedules.md`). Hypomnema
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

How the Claude process lives, compared with Hypomnema's runner:

| | Hypomnema (`internal/node/claude/runner.go`) | OpenMausBot (`drivers/claude.ts`) |
|---|---|---|
| Process lifetime | One process per Dispatch: one turn, then stdin is closed and it exits (L578) | One process per thread across turns: stdin stays open and the next message is a new turn; one sent mid-turn is folded into the running turn (L832-834) |
| Daemon restart | The worker runs under a shim with a journal and survives it | The process is the server's child; a turn cut by a restart is run again (`docs/runtime-contract.md`) |
| Extra flags | Few | `--include-partial-messages` (token streaming for chat), `--strict-mcp-config` and `--setting-sources project` so a turn does not pick up the machine's MCP servers, skills, hooks and CLAUDE.md ("one desktop: 407 tools, ~10k tokens", L1029), `--tools` and `--disallowedTools`, `--effort`, a CLI version check before each turn |

Whether a Hypomnema worker picks up the node user's MCP servers, skills and CLAUDE.md the same way was not checked
(inference: likely, since the runner passes neither flag).

### Finding CLIs and accounts

OpenMausBot asks each CLI rather than reading credential files:

- **CLIs:** found on PATH and probed with `--version`, run with PATH only and no inherited credentials
  (`server/cli-probe.ts`). A missing Claude or Codex CLI is installed into the app's own folder after asking
  (`server/engine-install.ts`). An ACP agent is checked by sending `initialize` alone, with no session
  (`server/engine-handshake.ts`).
- **Claude account:** `claude auth status --json` gives `loggedIn`, the email, the organization and the method.
  Credential storage differs by platform (Keychain, a JSON file) and a stale file still looks signed in, so only
  `loggedIn: false` counts as signed out and a timeout says nothing (`server/drivers/claude-auth-status.ts`).
- **Codex account:** `account/read` over the app-server gives the ChatGPT email; `auth.json` is not read, since it can
  carry an old login's email (`server/drivers/codex-identity.ts`).
- **Several Claude accounts:** one `CLAUDE_CONFIG_DIR` each; Claude owns the login, and removing an account in the app
  neither deletes its folder nor signs it out (`server/claude-accounts.ts`).
- **A test message** confirms an API-key connection; native CLI sign-in is confirmed without one, since a message may
  cost money (`server/engine-check.ts`, `docs/cli-onboarding.md`).

Hypomnema's `hypomnema-node check` already reports each CLI's path and version at every hello; accounts are declared by
hand (`accounts add`, `accounts set`) and their sign-in is not checked until a Task is blocked. The node could report
`claude auth status --json` per account (status and email only, never a token) at each hello (inference).

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

### How bots work together

- **A bot is a long-lived persona:** a name, standing instructions (`SOUL.md`), memory, skills and its own threads.
- **One tool for every hand-off**, `coordinate_bots(bot_ids, message, thread, reply, consult, done_when, answers)`,
  served by the "agents" MCP server and routed through the harness (`docs/runtime-contract.md` §9). A message to a
  thread where handed-out work is running is steered into that turn; one to a parked question answers it.
- **Chief of Staff:** one crowned bot per person. Its authority is exactly the bots that person owns: their incidents
  and escalations come to it, it retries their threads, changes their setup and can answer their questions for the
  person. Its approval level flows down to the work it delegates (`docs/team-cloud.md` L416,
  `docs/approval-levels.md` L123).
- **Rooms:** one shared transcript with several bots that @mention each other; a goal runs bounded rounds toward an
  objective, with a Room Lead (`docs/plans/2026-09-02-goal-mode-v2.md`, `docs/headless-cli.md` "Launch Room").
- **Groups are made by people, not by the bots:** teams and departments are created in the app, through the API, or
  from YAML (`openmausbot team apply`). "Departments are folders, not walls": every bot reaches every bot
  (`docs/team-cloud.md`). A Chief can add teammates to its own team.

How Hypomnema differs: one coordinator per Run plans a graph of Tasks; workers are short-lived hands with no persona,
and talk only to the control plane. The long-lived part is the coordinator and its Chat, plus shared memory later
(`docs/vision.md`). Messages between workers are studied in
[a separate note](2026-11-09-worker-to-worker-messages.md).

## 5. UI and UX

Read from the screenshots in `docs/screenshots/` and the code in `src/`.

- **Agents as contacts, work as threads.** The sidebar is a contact list; the header has a thread picker ("Forge ·
  Release build ⌄") and a Threads button (`moca-148-stop-ios.jpg`).
- **Question cards** (`approval-card.png`): a question, options A–D, a "Type your own answer" field and a close
  button. This is the shape of a Hypomnema question (an option or free text).
- **Cards that say why they no longer act** (`moca-282-expired-card-ios.jpg`): "The routine changed after this
  proposal was made… Expired — ask for a fresh proposal".
- **Where a message came from**: a "Webhook task" bubble that opens its payload (`activity-full-bubbles-ios.jpg`).
- **A composer that knows a turn is running**: the placeholder becomes "Sends after this turn", with Stop beside it;
  progress is short bubbles ("I found the failing check") and a typing indicator, not raw logs.
- **Empty states that define terms** (`tasks-routines.png`): "Task = one conversation and result. Routine = …".
- **Suggestion chips** under a reply: "Show diff", "Run tests", "Explain steps".
- **Onboarding** (`docs-engine-detection.png`): five steps; "Connect an AI" lists connections already found and signs
  in on the vendor's own page.

### What the bots can show

- **Widgets** (`show_widget`, `server/drivers/agents-widget-tools.ts`): the bot sends one JSON tree (Card or ListView
  of Row, Col, Text, Markdown, Badge, Progress, Chart, Button, Form with Select, RadioGroup, DatePicker, TimePicker,
  DayPicker, Input…). A press arrives as the person's next message; a widget can be replaced in place by its id (a
  progress bar moving); `fallback_text` serves clients that cannot draw it. Rendered by `src/components/widget/`.
- **Inline HTML and MCP Apps** (`show_html`, `docs/superpowers/specs/2026-10-14-chat-apps-design.md`): a
  self-contained page or an MCP server's `ui://` resource runs in a locked sandboxed frame inside the chat row and
  talks to the app over the MCP Apps bridge (`@modelcontextprotocol/ext-apps`, SEP-1865). No network beyond what it
  declared, no navigation, no acting as the person without an interaction.
- **Data**: charts with Vega-Lite (`src/components/data/DataChart.tsx`, `widget/WidgetChart.tsx`), tables with AG Grid
  (`DataGrid.tsx`), a SQL editor (CodeMirror) over DuckDB.
- **The computer's live screen** with noVNC (`src/components/LiveDesktop.tsx`, `DesktopViewer.tsx`).
- **Markdown** with react-markdown, remark-gfm, KaTeX, Mermaid diagrams and Shiki code highlighting.

### Stack (`package.json`)

React 19, Vite, Tailwind 4 with `clsx` and `tailwind-merge`, lucide-react icons, TanStack Virtual for long lists,
their own components (no shadcn or Radix), one server-backed store with one reducer, HTTP commands and one SSE stream
(`README.md`); Electron for the desktop shells; vitest, happy-dom and oxlint. Charts and data: Vega-Lite, AG Grid,
DuckDB, CodeMirror. Hypomnema's web stack (Vite, React, TanStack Router, Query and Form, Tailwind 4, shadcn on Base UI;
[decided](2026-11-09-directories-sessions-web-stack.md)) shares the base; the widget, chart and MCP Apps layers would
sit on top (inference).

## 6. Smaller things

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
- **Incidents reach the coordinator first** (`docs/team-incidents.md`): a run that fails, stalls (twenty minutes
  without activity) or cannot start opens, once, a "Problems to look at" thread on the Chief of Staff and gives the
  Chief a turn with who broke, the last request and reply, and what to do next, marked as not from the person.
- **Usage-limit takeover** (`docs/verification/automatic-recovery.md`, `server/host/limit-takeover.ts`): at a usage
  limit a conversation moves to the first backup engine or model that can take over, and goes back when the limit
  resets. A start-up failure is retried elsewhere only after the process has stopped and before any activity.
- **A smaller tool list** (`docs/agents-tool-bridge.md`): instead of 27–39 tools (43–57 KB), a turn is listed the few it
  usually needs plus `find_tool` and `use_tool`; the list shrinks by 65–74% and what a turn may call is unchanged.
- **Spend cap** (`docs/verification/spend-cap.md`): new turns are refused once the month's cost reaches the cap, with
  one warning at a set percentage; a turn counts the moment it settles.
- **Waits** (`docs/verification/waits.md`): a bot sets a condition once ("tell me when report.txt is there"); plain
  code checks it on a timer and no model runs until it fires; a wait never renews itself.
- **Saved steps** (`docs/verification/saved-steps.md`): a computer task done once at full access is replayed for the
  same words without a model call, each target found again and each action approved as usual.
- **Memory as plain markdown** (`docs/memory.md`): files the person can edit, a gauge of how much loads into a turn, a
  journal of every change with undo.
- **Driven from scripts and CI** (`docs/headless-cli.md`, `docs/mcp-server.md`): set up a team from a file, give a job,
  wait until it and every teammate it asked are done; a stdio MCP server exposes a bounded control plane (no approval
  grants, deletion, settings or credentials).
- **One verification surface** (`docs/verification/README.md`): every feature has a page of automated checks plus a
  fixture run through the same CLI and MCP tools external clients use (`pnpm control:omb`), with a fake engine.

## 7. What applies to Hypomnema

All of this is inference, for later issues; nothing is decided here.

1. Spike bubblewrap on the first node as the worker's sandbox (section 3). Highest value: it addresses what issue #32
   left out.
2. Read the reset time from Claude's `rate_limit_event` for account cooldowns (section 2).
3. Take origin-at-the-root, `consult`/`notify` and the continuation progress rule into the coordinator's design
   (section 4).
4. Keep one worker process per Conversation between Dispatches only if start-up cost shows up in measurements; today
   each Dispatch resumes the session, which is the state OpenMausBot falls back to after its idle timeout.
5. Pass `--strict-mcp-config` and `--setting-sources project` to workers, once it is checked that they pick up the
   node user's configuration today (section 2).
6. Report each account's sign-in (`claude auth status --json`) at each hello, so the web chat shows a lapsed login
   before a Task is blocked (section 2).
7. For the web chat (#41): question cards with an own-answer field, cards that say why they closed, a composer that
   knows a turn is running, and later widgets that a worker or the coordinator can show (section 5).
8. Messages between workers: issue #55 ([note](2026-11-09-worker-to-worker-messages.md)).

## Sources

- OpenMausBot: https://github.com/milind-soni/OpenMausBot
- Muse: https://about.fb.com/news/2026/09/introducing-muse
- Grok Bot and Muse compared:
  https://forkast.news/same-blueprint-different-locks-grokbot-and-muse-ship-the-persistent-agent-vm/ ·
  https://aiidelist.com/blog/grok-bot-vs-muse-vs-dots
- Grok Bot: https://ai-solutions.daviesmeyer.com/en/blog/grok-bot-ki-teammates-xai
