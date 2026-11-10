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
| Platform rules | A cap on dispatches (`spawn_bounds`, 6 a turn), a deny list for destructive commands (`blast_radius`), allowed purposes | Polly `guardrails` | The scheduler's limit of 4 unsettled Dispatches |
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
