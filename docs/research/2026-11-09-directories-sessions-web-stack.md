# Coordinator and worker directories, viewing worker sessions, and the web stack

Date: 2026-11-09. Read from the clones in `.tmp/` and the owner's repositories (MemoryOS, BeyondPilot, second-bite,
northstar). Paths are relative to each clone.

The context: the first real runs on aioffice-app (issue #32) worked. The coordinator (pi) and the web chat are next.
This note covers three questions: where the coordinator and the workers work, how the owner sees a worker's session,
and which stack the web client uses.

## 1. Where workers work

| Reference | Directory | Branch | Created and removed by |
|---|---|---|---|
| Orca | `<workspaceDir>/<repo>/<name>`, default `~/orca/workspaces` (`src/main/ipc/worktree-logic.ts` L77-89, `src/shared/constants.ts` L86-90) | `<git user>/<name>` (`src/shared/git/branches/branch-prefix.ts`) | Orca (`worker-worktree-creation.ts` L108-140); the user deletes it (`docs/site/content/docs/model/worktrees.mdx` L15-23) |
| OpenClaw | `<stateDir>/worktrees/<repo fingerprint>/<name>` (`docs/concepts/managed-worktrees.md` L161-177) | `openclaw/<name>` | The gateway; kept by default and capped by `worktreeMaxCount`, snapshotted before removal (`docs/tools/subagents/tool-reference.md` L233-279) |
| genie | `<repo>/.claude/worktrees/wish-<slug>` (`skills/wish/references/native-fallback.md` L126-135) | `wish/<slug>`, from `origin/<base>` | The workflow; never deletes; the operator removes it after merge (`skills/wish/SKILL.md` L32) |
| Omnigent (Polly) | `.worktrees/<task>` (`examples/polly/skills/fanout/SKILL.md` L12-21) | `polly/<task>` | The coordinator; removed once its PR is open (same file L44); writes outside it are denied (`omnigent/policies/builtins/orchestration.py` `worktree_guard`) |
| Hermes | `<repo>/.worktrees/subagent-<id>` (`tools/subagent_worktree.py`) | `hermes-subagent/<id>` | Removed when clean; otherwise archived to `refs/hermes/archive/*`; idle trees swept after 24 h (`hermes_cli/config_defaults.py` L466-480) |
| Omnara | No worktrees from the platform; the example prompt asks for `git worktree add /workspace/<name>` (`examples/slack-coding-agent/agent.yaml` L69-75) | — | The machine is released |
| Hypomnema today | `<worktree root>/<run>/<task>` from a bare mirror (`internal/node/worktree`) | `hyp/<task>` | The node; never removed |

The common pattern: one git worktree per task on a prefixed branch, created by the system rather than by the agent,
and kept so follow-up work can continue there. Hypomnema matches. **What is missing is removal.** Most of the
references keep the worktree until the change is merged or abandoned: genie and Orca leave it to the operator, and
Hermes sweeps idle ones. The proposal is to remove a worktree once its Task is finished and its pull request is merged
or closed.

## 2. Where the coordinator works

- OpenClaw gives the main agent a home, `~/.openclaw/workspace` (`src/agents/workspace-default-path.ts` L8-25). It
  holds `AGENTS.md`, `SOUL.md`, `TOOLS.md`, `MEMORY.md`, daily notes in `memory/` and `skills/`
  (`src/agents/workspace-bootstrap-policy.ts` L11-17). That home is also the cwd for its tools. Sessions are stored in
  `~/.openclaw/agents/<id>/agent/openclaw-agent.sqlite`.
- Hermes is similar: `~/.hermes` holds `SOUL.md`, `memories/` and `skills/`, and sessions are in `state.db`
  (`hermes_constants.py` L57-65, `hermes_state.py` L181).
- Orca stores orchestration state in `<userData>/orchestration.db`. Omnara keeps agents as event rows in Postgres
  (`migrations/000004_events_messages_turns.sql`).

Does the coordinator edit code? The references split:

- The dedicated orchestrators, **Polly, Orca and genie, do not**.
  - Polly: "you do NOT write code", and deep investigation goes to a read-only `explore` sub-agent
    (`examples/polly/config.yaml` L33-47, L148-155). Docs and text edits are its own.
  - Orca: "let `worker-start` create a worker's workspace" (`skill-guides/orchestration.md` L62).
  - genie: only its Work and Repair stages write source (`skills/wish/references/native-fallback.md` L9).
- The general assistants, **OpenClaw, Hermes and Omnara, may**. For them, delegation is optional.

Proposal:

- pi runs as the `coordinator` user. Its home holds pi's own directory (`PI_CODING_AGENT_DIR`) and a working
  directory with its rules, memory and skills, after OpenClaw. That working directory is pi's cwd.
- pi keeps no repository checkout and changes no code, after Polly, Orca and genie. To understand code, it dispatches a
  read-only explore Task.
- The conversation with pi is stored in the control plane's database, as Omnara does. This agrees with spike A: the
  state lives outside pi.
- Inference, as no reference does exactly this: pi does not edit docs either for now. Every change goes through a
  worker and a pull request.

## 3. Viewing a worker's session

The 2026-11-07 conversation note already says the web chat can open a worker's Conversation. Today the full stream
stays in the node's journal (`run/<dispatch>/journal`). The control plane receives only the start, liveness,
questions and the final report.

How the references show a worker's session:

- Omnara stores every agent as events in the server's database (`agent_events`) and streams them to the web.
- Omnigent's coordinator and web read a session's history from the server (`sys_session_get_history`).
- OpenClaw keeps each sub-agent as its own session in the gateway's database.

Proposal, after Omnara: the node sends the journal lines through its existing outbox. The control plane stores them
as the Dispatch's events, and the web reads them over SSE. A reconnecting node then loses no lines.

## 4. The web stack

| | Build | Routing, data | UI | Lint, test | Package manager |
|---|---|---|---|---|---|
| Omnara `frontend/apps/web/package.json` | Vite, React | TanStack Router, Query, Form | shadcn, Base UI, Tailwind | vitest, Playwright | pnpm |
| Omnigent `web/package.json` | Vite, React | react-router, TanStack Query, zustand | shadcn, Radix, Tailwind, streamdown, ai-elements | vitest, oxlint, Storybook | pnpm |
| MemoryOS `web/package.json` (owner) | Vite 8, React 19 | TanStack Router, Query, Form | shadcn on Base UI, Tailwind 4, assistant-ui, streamdown | vitest, Playwright, oxlint, oxfmt; hey-api for the client | pnpm |
| BeyondPilot, second-bite `web/` (owner) | Next 16, React 19 | TanStack Query, Form | shadcn on Base UI, Tailwind 4 | vitest, Playwright, oxlint | pnpm |
| northstar `web/` (owner) | Vite 8, React 19 | TanStack Router, Query | shadcn, Radix, Tailwind 4, streamdown, AI SDK | vitest, Playwright, oxlint | — |

**Decided by the owner on 2026-11-09**, with the latest stable versions at build time:

- Vite, React and TypeScript;
- TanStack Router (file routes), Query and Form;
- Tailwind 4 and shadcn on Base UI;
- pnpm, oxlint, oxfmt, vitest and Playwright;
- an API client generated from the OpenAPI contract (hey-api, as in MemoryOS).

The reason: the control plane serves a single-page app over its own API, as Omnara's and MemoryOS's setups do. Next
would add a Node server to operate.

The chat components are chosen with the web chat design. The options are MemoryOS's assistant-ui with streamdown and
Omnigent's ai-elements with streamdown. The design starts phone-first, as Omnigent's web UI is (`docs/vision.md`).

## 5. The look

What the references set in shadcn's `components.json` (read in `.tmp/`):

| Reference | Style | Base color |
|---|---|---|
| Omnigent `web` | `radix-nova` | neutral |
| Orca | `new-york-v4` | neutral |
| Omnara, Coder, Bifrost, Buzz | `new-york` | zinc |
| Hermes desktop, companion | `new-york` | neutral |
| LiteLLM | `base-vega` | gray |

OpenClaw and opencode use Solid, not shadcn.

None uses a tinted base color. The primary color differs:

- Orca keeps it neutral (`src/renderer/src/assets/main.css`: `--primary: #e5e5e5` in dark mode).
- Omnara uses blue (`frontend/apps/web/src/styles/palette.css`).
- Omnigent uses its brand pink by default. Its default palette is `omni` (`web/src/lib/themePalette.ts` L47, `--primary: #df3c85`), and the user can pick seven other palettes.

Omnigent also adds its own tokens on top of shadcn's (`web/src/index.css` L259-276):

- `--status-blue`, `--status-green`, `--status-yellow`, `--status-red` and `--status-gray`;
- `--success`, `--warning` and `--info`;
- `--link`, `--border-strong`, `--code-bg` and `--sidebar-active`.

affectUI's agent orchestration screen (read from its CSS on 2026-11-09) uses:

- Inter at 13px;
- dark surfaces in steps (`#0e0e10`, `#141416`, `#1b1b1e`) with 8% white hairlines;
- four text levels made by white opacity;
- one blue accent;
- status pairs, each a text color on a tinted background (running, done, needs you, failed).

**Decided by the owner on 2026-11-09:**

- **Base:** the shadcn preset `b43eDcOJc` (Nova, base color neutral, theme and charts pink, Inter, Lucide, small radius). It is applied with `shadcn init --preset b43eDcOJc --base base`. Dark is the default, and light follows the system.
- **Brand color:** pink, on Radix Colors' scale rather than shadcn's brighter one: `#d6409f` for the primary fill, and `#ff8dcc` for links and accents on dark.
- **Tokens added in `index.css`**, after Omnigent:
  - five statuses: running (blue), done (green), needs you (amber), failed (red leaning orange, as affectUI's `#ff977d`, so it does not read as the pink brand), and queued (gray). Each has a text color and a tinted background, for light and dark.
  - `--link`, `--border-strong`, `--code-bg` and `--sidebar-active`;
  - a mono font for ids, branches and logs.
  - A status is never shown by color alone: it carries an icon and a word.
- **Type:** 13px on wide screens; inputs are 16px on phones, as iOS zooms into smaller inputs.
- **Not now:** user-selectable palettes, as Omnigent has; there is one user.
