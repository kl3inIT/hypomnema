# The coordinator: routing work, default agents and what makes it capable

Date: 2026-11-10. Asked by the owner: which pi extensions the coordinator should have, what omp has that pi lacks,
whether a strategy exists for routing a Task to Claude Code, Codex or omp and for choosing its model (with Jev and
Luna), which agents the references ship by default, and what OpenMausBot is used for and how it routes.

Read in the clones under `.tmp/`: omp at 660a31d, pi at 2a32a60 (1.5.0), openmausbot at 1de1164, omnigent at cf1021e,
openclaw at d622a2ba4. Paths are in those repositories. omp, pi and the default agents were read by two scouts;
Polly's config, OpenMausBot's decision model and presets, and Hypomnema's own files were read directly. Inferences
are marked. Nothing is decided here.

## 1. Hypomnema today

- **pi runs bare.** `deploy/coordinator/package.json` pins `@earendil-works/pi-coding-agent` 1.5.0 and nothing else;
  no extension, package or MCP server is installed. Its instructions are `deploy/coordinator/AGENTS.md` (12 lines)
  and the `hypomnema-coordinator` skill.
- **Routing is one sentence** of the skill's guide (`internal/controlplane/clientcli/skills/hypomnema-coordinator/guide.md`):
  choose a profile by its description, and leave the model to the profile unless the owner names one.
- **Every worker profile is the same runtime.** `implementer`, `reviewer` and `explorer` run Claude Code with its
  default model (`orchestration/migrations/00009_profile_description.sql`); `coordinator` is pi. So there is nothing
  to route between yet.
- **Only Claude Code runs as a worker.** omp and opencode are issue #107, open.
- **Tool limits for workers were declined** by the owner (issue #50, closed 2026-11-09): each Task has its own
  worktree and every pull request is reviewed.

## 2. Jev, Luna and pi's virtual models

- Jev is TypeSafe's classifier model: it does not chat, it answers typed questions (a choice, yes or no, a score)
  with probabilities (`pi/packages/coding-agent/docs/models.md` L144-152).
- pi 1.5.0 has experimental virtual models: an extension registers a model name and picks a real model and thinking
  level for each request (`docs/virtual-models.md`, `pi.registerVirtualModel`).
- `examples/extensions/jev-router.ts` registers `jev/auto`: Jev rates the first user message, planning runs on
  GPT-5.6 Sol when it is complex and Terra otherwise, and after the first successful `edit` or `write` the session
  moves to Luna and stays there. It needs `TYPESAFE_API_KEY` and a Codex login inside pi.
- This switches the model **inside one pi session**. It does not choose which worker or which CLI gets a Task.

## 3. How the references route

| Kind | Reference | What it does | Where |
|---|---|---|---|
| Guidance for the coordinating model | Omnigent's Polly | Which CLI: "`claude_code` for multi-file / refactor / test-writing work, `codex` for narrow, well-scoped changes"; `review` / `explore` / `search` go to `pi`; "review is ALWAYS done by a DIFFERENT vendor than the implementer". Which model: `args.model` per dispatch, "cheap and fast for explores and wide fan-outs, strongest for hard implementation or final review" | `examples/polly/config.yaml` L62, L97-100, L175-186 |
| Guidance | Orca | "Inherit the active model unless the user or an evidenced capacity diagnosis authorizes a change"; no rule from task type to CLI | `skills/orchestration/SKILL.md` L18 |
| Static role to model | omp | An agent's frontmatter names a role alias (`scout`: `@smol`, `reviewer`: `@slow`); `modelRoles` maps each alias to a model in one place (`default`, `smol`, `slow`, `plan`, `task`, `commit`, `advisor`, `judge`…) | `packages/coding-agent/src/prompts/agents/*.md`, `src/config/model-roles.ts` L56-91 |
| Static | OpenClaw | `agents.defaults.model` with a primary and fallbacks; `agents.defaults.subagents.model`; an explicit `sessions_spawn.model` wins | `docs/tools/subagents/tool-reference.md` L68 |
| Static | OpenMausBot | The person picks each bot's model; a preset "has no field for a model" | `docs/presets.md` |
| Automatic | omp | A classifier picks the thinking level of each prompt, never the model | `src/auto-thinking/classifier.ts` |
| Automatic | Omnigent | `sys_advise_models`: given the planned tasks, recommends a model for each from its description's difficulty; Polly is told to use it as `args.model`. It picks no CLI. The router behind it was not read | `omnigent/tools/builtins/advise_models.py`, `examples/polly/config.yaml` L110-114 |
| Automatic | OpenMausBot | The decision model (Jev by default, or Clef, or OpenRouter) picks which bot answers a room message that mentions nobody, from each bot's name, title and description. One bot at 0.6 or more answers; anything less sure goes to the room's lead; the answer is awaited 1.5 s; every failure does what the room did without it; each call is logged without its text. "Tool selection and where work runs" are listed as coming soon | `docs/decision-model.md` |
| Failover | OpenMausBot | At a usage limit a conversation moves to the first backup engine or model and returns when the limit resets | `server/host/limit-takeover.ts` |

No reference chooses a CLI by classifier. The CLI is chosen by the person or by the coordinating model from written
guidance; a classifier, where there is one, picks among agents by their descriptions (OpenMausBot) or sizes a model
(Omnigent), and fails open to the default.

## 4. Agents shipped by default

The roles of omp, Polly, genie, Omnara and OpenClaw are in
[agent roles and tool limits](2026-11-09-agent-roles-and-tool-limits.md). Added here:

| Reference | Shipped | Where |
|---|---|---|
| Omnigent | `polly` (coding orchestrator over seven CLI sub-agents), `debby` (two vendors debate), `deep-research`, `remy` (long-term memory), `scribe` (docs), `sentinel` (security review), `aws_analyst` | `examples/*/config.yaml` |
| OpenClaw | One agent, `main` | `docs/concepts/multi-agent.md` L382 |
| OpenMausBot | One Chief of Staff per person and the person's bots; ready-made bots come from presets (name, instructions, skills, starter notes) | `docs/team-cloud.md` L416, `docs/presets.md` |
| Hermes | One agent whose persona is `SOUL.md`; no personalities shipped | `cli-config.yaml.example` L1408 |

Hypomnema's `implementer`, `reviewer`, `explorer` and `coordinator` match the common coding set. Personas for work
that is not code exist only in the personal-assistant products.

## 5. What OpenMausBot is for

A chat app of personal bots, not a software factory (`README.md`, `docs/`): each bot is a contact with a persona,
memory, a model, a computer (a screen and a shell) and connected apps (Gmail, Slack, GitHub, Notion through
Composio); routines on a schedule and proactive ones that read an inbox; rooms where several bots work toward a goal;
a Chief of Staff that receives incidents; meeting notes, voice, image generation. Coding is one use, done by the
person's own Claude Code or Codex. More in [the earlier note](2026-11-09-openmausbot-and-personal-agent-vms.md).

## 6. What makes a coordinator capable

omp builds in what pi leaves to extensions: subagents and a task tool (`src/task/`), agent definitions, model roles,
plan mode, memory (`src/memories/`, `src/memory-backend/`), web search (`src/web/`, `src/exa/`), LSP, goals, an
advisor, auto-thinking, and a plugin system with a marketplace (`src/extensibility/plugins/`). pi is extended with
extensions (TypeScript: `registerTool`, `registerProvider`, `registerMcpServer`, the `tool_call` gate,
`before_agent_start`), packages, skills and MCP servers (`docs/extensions.md`, `docs/mcp.md`). In `--mode rpc`
dialogs travel over the JSON protocol and custom components are ignored (`docs/extensions.md` L3260).

Polly, the one coding coordinator among the references, gets its strength from three layers, and extensions are the
thinnest:

| Layer | What | From | Hypomnema |
|---|---|---|---|
| Skills | `investigate` (answer from read-only sub-agents' reports), `fanout` (independent tasks in parallel, a PR each), `cross-review` (the other vendor reviews the diff; blocking issues become fix tasks) | Polly `examples/polly/skills/` | One skill; none of these |
| Skills | A roster check before the first dispatch: which workers can start on this host | Polly `config.yaml`, "Roster preflight" | Not in the skill; `nodes list` and `profiles list` hold the facts |
| Platform rules | A cap on dispatches (`spawn_bounds`, 6 a turn), a deny list for destructive commands (`blast_radius`), allowed purposes | Polly `guardrails` (`omnigent/policies/builtins/orchestration.py`) | The scheduler's limit of 4 unsettled Dispatches |
| Tools | Web search and fetch | OpenClaw `web_search`, `web_fetch`; omp `src/web/` | None; pi's docs name no web tool |
| Tools | A structured question to the person | OpenClaw `ask_user`; OpenMausBot's question cards; pi `examples/extensions/question.ts` | None for the coordinator; the web answers workers' questions |
| Platform | Memory across conversations | OpenMausBot `MEMORY.md` (`docs/memory.md`); OpenClaw active memory; omp memories | Issue #21, "later", written for nodes and workers; issue #41 says pi's session file is its memory, which is one chat |
| Platform | Work on a schedule | OpenClaw `automations`; OpenMausBot routines | A line of intent in `docs/vision.md`; no issue |

Polly's "you do NOT write code" is a prompt rule; its guardrails do not forbid edits. With issue #50's reasoning, an
extension that blocks the coordinator's writes has no reference behind it and is left out.

## 7. Read for Hypomnema

All inference, for the owner to decide.

1. **Routing by profiles and guidance, not a classifier** (Polly): a profile per runtime with a description that
   says when to use it, and the cross-vendor review rule in the coordinator skill. The choice by description already
   exists. It needs issue #107 first: until a second runtime runs there is nothing to route.
2. **Model by role, set in the profile** (omp, OpenClaw): a cheap model for `explorer`, the strongest for `reviewer`.
3. **Three coordinator skills and the roster check** (Polly). These need no second runtime, except `cross-review`.
4. **The coordinator's memory across chats** named in issue #21's scope (OpenMausBot, OpenClaw), so its design does
   not cover workers only.
5. **Jev or a model advisor: not now.** The one use a reference has for a classifier in routing (OpenMausBot) is a
   choice among agents by description, which pi already makes as it plans; a model advisor (Omnigent) needs the cost
   figures of issue #22.
6. **Web tools and a question tool for the coordinator**: later, each when a need shows.

## 8. Second reading: thirteen more references (2026-11-10)

The owner asked for a wider comparison before any plan: section 7 rested mostly on Polly. Four scouts read omp,
openhands-sdk, coder, hermes-agent, orca, genie, openclaw, openmausbot, the rest of omnigent, companion,
durable-harness, buzz and t3code at the revisions above (orca, genie, coder, hermes-agent, openhands-sdk, t3code,
companion, buzz and durable-harness as cloned in `.tmp/` on this date). OpenMausBot's `docs/runtime-contract.md`,
`team-incidents.md` and `team-escalations.md` were read directly. Correction to section 6: Polly's policies are in
`omnigent/omnigent/policies/builtins/orchestration.py`; `omnigent/inner/nessie/policies.py` re-exports them.

### 8.1 Who chooses the worker and the model

| Reference | The worker | The model | Where |
|---|---|---|---|
| omp | The model, told "Use most specific agent"; an advisory when it spawns generic workers where a specialist fits | Fixed per agent by role (`scout` `@smol`, `reviewer` `@slow`). A model per call is off by default; when on, only operator-approved models, and "Requested model unavailable → stop and report; NEVER substitute another". A judge picks the effort only | `prompts/tools/task.md` L12, L21-25; `task/settings.ts` L324-402; `auto-thinking/classifier.ts` |
| Hermes | No kinds of sub-agent | Inherits the parent's unless pinned in config; the `advanced-delegation` plugin lets the model pick among named options it sees only by name and description (`fast`, `deep`); an unroutable task is refused | `cli-config.yaml.example` L1927-1934; `website/docs/user-guide/features/delegation.md` L298-332 |
| Coder | The model picks `general`, `explore` or `computer_use`, with written guidance | Admin or personal default per context (root, general, explore, advisor, titles); optional `model_config_id` and effort per spawn | `coderd/x/chatd/subagent_catalog.go` L16-46, L297-336 |
| OpenHands SDK | The model picks `subagent_type` from injected descriptions | Fixed per agent definition, `inherit` by default; no model argument | `openhands-tools/.../task/definition.py` L58-144; `sdk/subagent/schema.py` L211-213 |
| Orca | The coordinating model names `--agent` on each start | "Add `--model` only if the user named one" | `skill-guides/orchestration.md` L5-12, L45 |
| genie | Portable roles in the plan (`engineer`, `reviewer`, `fix`) | "Inherit the active model unless the user or an evidenced capacity diagnosis authorizes a change"; the saved workflow fixes tiers: scout, gate and publisher on the cheaper model, judge, executor, review and fix on the stronger | `skills/work/SKILL.md` L18; `.claude/workflows/wish.js` L1436-1443; `skills/fix/SKILL.md` L31 |
| OpenClaw | Config; an external CLI only "If user asked for a specific agent" | `agents.defaults.subagents.model`, with the note "set a cheaper model for sub-agents"; fallbacks in order | `docs/tools/subagents/tool-reference.md` L68; `skills/coding-agent/SKILL.md` L50 |
| OpenMausBot | The model picks a bot from `list_bots` (department, model, status): "Bring a teammate in when your own task genuinely needs what they know" | Each bot keeps its own. At a usage limit: that AI's default model, then the person's backups in order (up to five), skipping one that is not ready or is on the same plan | `server/drivers/agents-catalog.ts` L227-260; `server/peer-roster.ts` L298; `server/host/limit-takeover.ts` L158-194 |
| Omnigent | The orchestrator, from its prompt | `sys_advise_models`, registered only when a router is configured: a judge sorts each task SIMPLE, MODERATE or COMPLEX and returns the first, middle or last model by cost; 9 s; no verdict gives `model: null` | `server/smart_routing.py` L491-643; `server/routes/_sessions/helpers.py` L11630-11788 |
| t3code | `delegate_task` names a target or inherits the parent's | The same | `docs/orchestration-v2/orchestrator-mcp-server.md` L228-256 |
| companion, buzz, durable-harness | Config or code | Config or code | `companion/apps/server/src/executor.ts` L292; `buzz/crates/buzz-acp/README.md` L116; `durable-harness/packages/durable-harness/README.md` L30-46 |

Common to all: the coordinating model chooses **which kind of worker** from names and descriptions; the **model is
set ahead** (per role, per agent or inherited) and changes per task only inside limits the operator set; a model
that is not available stops the work and is reported, not replaced silently (omp, genie, Hermes). Cheap for
exploring and strong for judging is how omp, genie, Hermes, OpenClaw and Coder set their tiers.

### 8.2 Review

| Reference | When | Who | Given | Returns | Fixes |
|---|---|---|---|---|---|
| genie | Every group | "A different reviewer", read-only | Frozen criteria written before the diff is opened, and the exact SHA or diff; "not the author's explanation as proof" | `SHIP` / `FIX-FIRST` / `BLOCKED`; CRITICAL and HIGH block, MEDIUM and LOW do not | The `fix` role, reusing the worker's context; 2 loops by default, 3 at most, then a human decides (`skills/review/SKILL.md` L10-70, `skills/fix/SKILL.md` L10-33) |
| Polly | Every pull request | A different vendor | The diff file and the contract | Blocking, non-blocking, suggestions | Back to the same implementer conversation; "a few loops", then the person |
| Scribe, Sentinel (Omnigent) | "When accuracy matters" | A different vendor | The draft | Not read | Finalize without it when the reviewer cannot start (`examples/scribe/config.yaml` L83-92) |
| omp | On request (`/review`); 1 to 16 reviewers by the diff's size | `reviewer`, no edit tool | The diff | `overall_correctness` (correct or incorrect), `confidence`, findings with `priority` P0-P3, file and lines that "MUST overlap diff"; only issues with provable impact that the patch introduced | "spawn corrective subagent specifying the gap; NEVER silently fix it inline" (`prompts/agents/reviewer.md`, `prompts/system/orchestrate-notice.md`) |
| OpenClaw | Optional plugin, or a `reviewer` role in the team preset | A reviewer in an empty workspace with no host tools | Not read | Priorities P0-P3; blocking apart from optional | Through the coordinator; "after one clarified follow-up, report the blocker" (`docs/tools/autoreview.md`, `docs/reference/templates/roles/`) |
| Hermes | On request (`/review`) | A background sub-agent | The last 10 messages | A free-text verdict | Re-enters the session |
| Orca, t3code | On request | A worker of another agent; `role: "review"` | Not found | Not found | Orca: "Send fixes to a worker" (the same one) |
| OpenMausBot | No reviewer; every report is checked | The requester | `done_when` and what the harness observed of the turn | Not applicable | `rework=true`; a verifier panel is listed as later |
| OpenHands SDK, Coder | None shipped | | | | |

Two references make review part of every change (genie, Polly); the others run it on request or when it matters.
Where there is a reviewer it is never the author, it reads the diff and the criteria and not the author's account,
and it does not edit. P0-P3 is the one severity scale two references share (omp, OpenClaw).

### 8.3 A worker's report is a claim

| Reference | Rule | Where |
|---|---|---|
| Hermes | "Child summaries are SELF-REPORTS, not verified facts … verify it yourself" | `tools/delegate_tool.py` L735-738 |
| omp | "`completed`: subagent yielded successfully; claimed artifacts unverified"; the orchestrator verifies once per phase | `prompts/tools/task-async-contract.md` L7 |
| genie | "Resolve every claimed SHA with git … compare actual changed paths rather than trusting self-reported lists" | `skills/wish/references/native-fallback.md` L13 |
| OpenClaw | "A promise or unsupported completion claim is not a finished result" | `docs/reference/templates/roles/coordinator/workspace/AGENTS.md` L27-35 |
| OpenMausBot | The report carries what the harness observed (commands, files changed, successful tool calls, outward actions): "check claims against what was observed; redo only what the evidence does not cover" | `server/runtime/envelope.ts` L148-181 |
| Orca, Coder | Rerun checks "only for a gap or contradiction" | `skill-guides/orchestration.md` L73; `subagent_catalog.go` L322-325 |
| OpenHands SDK | "The agent's results are authoritative" except for judgment | `task/definition.py` L140-141 |

### 8.4 Knowing what can run, and limits

- **Availability.** Omnigent reports each CLI per host as ready, `binary-missing`, `version-too-low` or `needs-auth`
  and refuses a session on one that is not (`onboarding/harness_readiness.py` L904-1127); t3code's
  `orchestrator_capabilities` says missing adapter, disabled, missing executable or missing auth; OpenClaw's
  coordinator role calls `agents_list`, and its coding skill runs `codex login status` first; OpenMausBot's
  `list_bots` "is the authority on who is free right now", and `usage_limits` gives each account's percent used and
  reset time. Orca and genie have no list.
- **Fan-out.** Per turn: Polly 6, Omnigent 5 by default. At once: Coder 5 root and 10 sub, Hermes 10, OpenHands 5,
  OpenClaw 8 (5 per agent), omp 32, Orca 4. OpenMausBot: 1 to 4 bots a call, depth 4, and per root 64 requests and
  96 executions, renewed by progress.
- **Loops.** OpenMausBot ends work `failed(stalled)` after two continuations with no successful tool call or the
  same error three times (`server/runtime/tree.ts` L38-62); a thread is retried twice, the third report tells the
  Chief to stop, and five incidents in an hour mute the thread (`docs/team-incidents.md`). Orca breaks after three
  failed starts. omp stops a sub-agent at 1.5 times its budget of 200 requests.
- **OpenMausBot's contract** (`docs/runtime-contract.md`), beyond the above: the origin of work (person, routine,
  outside) is written once at the root and an outside brief "is not their authority"; `consult` (answer, act on
  nothing) and `notify` beside `work`; the opening of a delegated turn is written by the harness from stored data;
  a turn cut by a restart runs again once, and not at all when it already acted outward; a wake only on an outcome,
  a question or a stall, once per result; the Chief never answers a gate only a person can lift.

### 8.5 Where agents are managed

Only Omnara (profiles), Omnigent (custom agents) and OpenMausBot (bots) have a saved agent with a name,
instructions and a model; t3code and Orca configure the CLIs, OpenClaw a workspace per agent, Coder fixed role slots.

| | List | Edit | Versions | What can run | Defaults | Delete |
|---|---|---|---|---|---|---|
| Omnara | Cards: model and provider, "Version N", 30-day cost, instances, Launch (`components/agents/AgentProfilesSection.tsx`) | Builder and YAML side by side; model combobox, effort, instructions textarea, tools, skills, MCP, sub-agents with a model override, max depth | Immutable configs; only "Version N" shown | An unavailable model or effort blocks the save | Per sub-agent | Delete; running agents go on |
| Omnigent | Built-in as read-only cards; own as a table: name, description, harness, created, edited (`SettingsCustomAgentsSection.tsx`) | Description and instructions textareas, harness select, model as text, MCP; import as a bundle | A counter; a save on a stale copy is refused | A Harnesses page per host: Configured, Not installed, Missing credentials, Out of date, with Install, Update, Sign in | Per project: agent, model, effort | Warns "N session(s) still use this agent" |
| OpenMausBot | Contacts by department | Name, title, blurb; Soul (instructions); model picker with sign-in state, effort, up to 5 backups; approval level; tools | A history of changes, undo for Soul | "Sign-in required", "org blocked" in the picker | Defaults for new bots | Archive or delete; not the Chief, not the last bot |
| OpenClaw | A select in the settings sidebar | Identity; primary, fallback and decision model; markdown files; tool presets; skills | Not found | "Installed agents": Installed, Sign-in required, "Check again" | Agent defaults, utility model | Not the last agent |
| t3code | Provider instances with a status line and a switch | Name, runtime, environment, models | Not found | Available, Not authenticated, Not found; Refresh | Default and fallback model | The switch |
| Orca | A row per CLI | Command, args, environment, permission | Not found | Detected per host; Refresh | Default agent | Enabled or disabled |
| Coder | Fixed contexts | Model and effort per context | Not found | "Unavailable model" | A model per role | Not applicable |

Hypomnema's profiles are Omnara's shape (immutable versions, a name pointing at the current one, YAML) and are
managed from the CLI only; the web has no page for them. The menus note already proposes "Agents (Models,
Profiles)" in Settings ([menus and settings](2026-11-10-menus-and-settings.md)).

### 8.6 What section 7 gets wrong or misses

- Routing by description stands, now with eight references and not one. The runtime hint (Claude Code for work
  across many files, Codex for a narrow change) has two sources, both Omnigent's: Polly's prompt and its router's
  hints.
- The model set in the profile and not chosen per Task stands, with omp, Hermes, Orca, genie and OpenClaw. Missing
  from section 7: an unavailable model stops and is reported.
- Review "always" was Polly's rule alone; with genie it is two of ten. Missing: the reviewer's return format, and a
  bound on fix loops (genie 2, OpenClaw 1).
- Missing altogether: what can run now, told to the coordinator (Omnigent, t3code, OpenClaw, OpenMausBot); the
  page where profiles are managed; and OpenMausBot's rules for a report's evidence and for work that makes no
  progress.

## 9. Evals, and Anthropic's own guidance (2026-11-10)

### 9.1 How the references test a coordinator

Read by one scout in openmausbot, buzz, omp, omnigent (its `tests/` from git, by file name and header only), orca,
genie, openclaw, hermes-agent, t3code, openhands-sdk, pi and coder.

| Kind | Who | How | In CI |
|---|---|---|---|
| A scripted or fake model | OpenMausBot (`evals/`, tiers 1 and 2; `server/runtime/scenarios.test.ts`, 55 tests with invariants), Omnigent (`tests/e2e/test_polly_e2e.py`, the model chosen per worker, `spawn_bounds`), OpenClaw (`qa/scenarios/agents/`, `subagent-handoff.yaml`), genie (`scripts/brainstorm-workflow-behavior.test.ts`) | Scripted turns; the test asserts the tool calls, the dispatch targets, the shape of the hand-off tree and the limits | Yes |
| A guard on prompt and skill text | Orca (`src/cli/specs/bundled-guide-flags.test.ts`: every command in the guides parses), genie (`scripts/skills-lint.ts`, the `*-parity.test.ts` files), Hermes (`tests/ci/test_prompt_surface.py`: a committed snapshot of the system prompt and tools), Buzz and OpenMausBot (prompts pinned by sha256) | Deterministic | Yes |
| Live models with cases | t3code (`evals/mcp-tool-use`: 188 cases, 37 on orchestration, about $9 a run on Sonnet), OpenMausBot (`evals/live/`, a judge whose rubric is pinned by sha256, `minScore` 0.75), Buzz (Harbor, k samples), OpenHands SDK (`tests/integration`, a judge) | By hand or behind a switch | No, for cost |
| A labelled set for a classifier | OpenMausBot only (`server/decider/room-routing.eval.test.ts`: 53 messages, accuracy at least 0.85, at most 2 confident wrong picks, about $0.002 a run) | By hand | No |

- OpenMausBot's `evals/live/scenarios/live-delegation-lane.json` gives a Chief "Do not do the micro-task yourself"
  and requires its only tool call to be `coordinate_bots`, once.
- t3code's rules for a prompt eval (`evals/README.md` L32-43): never copy the prompt into the eval; a case must fail
  on the old text and pass on the new; a case fails when half or more of its samples fail; runs are compared with a
  baseline.
- No accuracy eval was found for Omnigent's `sys_advise_models` or omp's classifiers.
- Hypomnema has the second kind (every command in the skill parses, after Orca) and neither of the others.

### 9.2 Anthropic's guides on choosing and combining models

Pointed out by the owner. Read through a fetch agent on 2026-11-10; of the second page only the first half came
back verbatim, the rest as that agent's summary, marked (summary).

[Choosing the right model](https://platform.claude.com/docs/en/about-claude/models/choosing-a-model):

- The criteria are capabilities, speed, cost and effort: "Tuning effort is often a better lever than switching
  models."
- Two ways to start: efficiency-first (the smallest model, "Upgrade only if necessary for specific capability
  gaps") or capability-first ("Consider increasing efficiency by lowering effort or downgrading models over time").
- Its table gives the smallest model "subagent tasks" and high-volume work, and the larger ones complex agentic
  coding and large refactors.
- On deciding to upgrade: "Having a good evaluation set is the most important step in the process."

[Optimizing for cost and intelligence](https://platform.claude.com/docs/en/about-claude/models/optimizing-for-cost-and-intelligence):

- "High-volume work with outputs you can check: Try Claude Haiku 5.5 first."
- "The work exceeds one context window: Split the work and hand the parts to lower-cost models." Delegation did not
  pay for "one dependent chain" (summary).
- "Run at low effort and re-run failures at high: in Anthropic's tests, about 40% lower cost than everything at
  high, at the same pass rate."
- "Compare on cost per completed task, not per token." A multi-model configuration "must beat the single model's
  whole curve" (summary).
- An advisor "can help only when your model consults it".

What it means here (inference):

- The effort is the first lever, which is what issue #127 made reach a Claude Code worker.
- A cheap model for work whose result can be checked, and for exploring, agrees with omp, genie, Hermes, OpenClaw
  and Coder (8.1).
- "One dependent chain" not being worth splitting agrees with the guide's rule to keep steps that build on each
  other in one Task.
- New, and in no reference: a retry at a higher effort than the first attempt. Hypomnema already retries a failed
  Task up to three times, at the same effort. Whether to raise it needs the cost figures of issue #22.
- The figures are Anthropic's, measured through the API by price. Hypomnema's workers run on subscriptions, where
  the cost is the plan's limits.
