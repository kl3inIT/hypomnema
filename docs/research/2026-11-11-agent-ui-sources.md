# Sources for the UI of an agent product (2026-11-11)

Read after the owner asked whether affectUI is worth learning from, and which other sources have components for
agents. No design is decided here. A line marked "(page)" was read as text fetched from the site by a sub-agent,
"(picture)" was seen in a screenshot taken for this note, "(search)" is a web search result that was not opened.

## The sources

| Source | What it has for agents | License, revision | Read how |
|---|---|---|---|
| affectUI, free part | 23 components, among them `tool-call-card`, `decision-inbox`, `thinking-loader`, `progress-tracker`, `message-actions`; 2 blocks, 4 motion effects, 17 illustrations. Each is one `.tsx` with its CSS inside, no Tailwind | MIT; `.tmp/affectui` at `3c5df43` (2026-11-10), `github.com/flohoeller/affectui` | file names listed |
| affectUI, Pro | 43 agent components, 1 of them free (page, `/category/agents`): Permission Request, Approval Card, Decision Card, Agent Graph, Agent Tree, Trace Waterfall, Context Window, Agent Checkpoints, Autonomy Dial, Agent Inbox and others; 39 screens, among them Agent / Orchestration | Paid once: 249 USD a person, 699 USD a team (page, `/pricing`). No code to read | pages and pictures |
| AI Elements (Vercel) | 48 components on shadcn: `agent`, `plan`, `task`, `queue`, `tool`, `reasoning`, `chain-of-thought`, `checkpoint`, `context`, `confirmation`, `question`, `terminal`, `test-results`, `commit`, `file-tree`, `stack-trace`, `sources`, and `canvas`, `node`, `edge` for a graph | Apache-2.0; `.tmp/ai-elements` at `ca8be60` (2026-10-14), `packages/elements/src` | file names listed |
| assistant-ui | Chat, tool calls, generative UI; an approval card at `packages/ui/src/components/react/assistant-ui/elements/approval-card.tsx`; packages for ACP, opencode, pi and LangGraph | MIT; `.tmp/assistant-ui` at `27d4bb8` (2026-11-09) | file names listed |
| tool-ui | A shadcn registry of 24 components that show a tool's result | MIT (search) | not cloned |
| ui-for-ai | 19 components, an `AgentTimeline` and graph nodes for agents (search) | not checked | not cloned |
| 21st.dev | Single components from many authors: tool timeline, agent activity, tool approval (search) | varies | not opened |
| The products already studied: Orca, Omnara, Omnigent, Coder, t3code, opencode | Whole screens with the behaviour behind them | see each earlier note | code, in the earlier notes |

The search found no open library with a tree of sub-agents, a trace and a context meter together (search).
affectUI's list is the widest; AI Elements is the open one closest to the web client's base (shadcn, Tailwind).

## affectUI

What a component page holds, the same for each (picture, `.tmp/affectui-pages/`): a live preview, "When to use",
"When not to use", the props, a table of keys, then accessibility, motion, responsive behaviour and "Notes for AI".
For a Pro component the install command, the usage and the examples are hidden; the rest is readable.

Its design-system pages (`/design-system`, `/design-system/colors`, `/design-system/typography`) gave a heading and
a description but no token values in the fetched text (page); they were not photographed.

### Agent / Orchestration, a Pro screen

Picture: `.tmp/affectui-pages/agent-orchestration-preview.png`.

- A path bar (project, run id, a "LIVE" mark) with "Pause run" and "Stop" at the right.
- A canvas with the agents as a tree of cards joined by lines, the orchestrator at the top. A card has the agent's
  role above it, its name, a status badge, one line of its task, then its model, cost and duration.
- Under the canvas a panel with three tabs: "Trace", "Tokens & cost", "Events" (with a count of errors). Its head
  shows agents done of the total (10/17), the elapsed time, tokens and cost. Trace is a row per agent, indented
  as the tree, with bars on a time axis.
- An agent's card opens a panel with its tasks, context and cost; on a phone it is a sheet from the bottom (page).
- Statuses in the data: queued, running, waiting, done, failed, stopped. Shown as: Queued, Running, Needs you, Done,
  Failed, Stopped. "Status always comes with words ... the colour only repeats it" (page, Accessibility).
- A figure that is not known shows "–", never 0 (page).
- Keys: Tab moves between regions, the canvas is one stop; arrows walk the tree; Enter opens an agent; Esc closes
  the panel and returns the focus (page).
- When not to use: one agent with a few tool calls (their Trace Waterfall or Agent Run instead) (page).

### Permission Request, a Pro component

Picture: `.tmp/affectui-pages/permission-request-desktop.png`.

- Who asks (`agent`), what (`action`, `target`), why in the agent's words (`reason`), and what will happen
  (`preview`: files, recipients or amounts; 3 rows, then "N more").
- `risk`: safe, risky or destructive. Destructive turns Allow red and leaves out "always".
- `scopes`: once, timed (60 minutes by default) or always. The button names the choice: "Allow for 1 hour".
- After the answer it folds to one line with the time left and "Revoke" (seen in the picture: "Allowed for 1 hour
  · push to main, 56 min left · until 01:47 AM, Revoke").
- Enter allows, Esc denies, the arrows pick how long.
- "The component only asks – store the decision and check it where the agent acts" (page, Notes for AI). It has
  nothing on how a grant is stored, expires or is revoked on a server.
- When not to use: setting how much an agent may do in general (their Autonomy Dial), reviewing a finished result
  (Approval Card), connecting a tool for the first time (the provider's own consent screen).

### Approval Card and Decision Card, Pro components (page)

- Approval Card: one pending action with a risk badge, details and a preview, Approve, Deny and an optional Edit,
  an "Always allow this action" checkbox, and a countdown that denies with the reason "timeout" and pauses while
  the card is hovered or focused.
- Decision Card: an agent's question above the chat's composer, with numbered options (one marked Recommended), a
  row for the user's own words, the keys 1 to 5, and "Later" to fold it.

### Tool Call Card, free

The code is at `.tmp/affectui/components/tool-call-card.tsx`; it was not read for this note. The page: one line
with the tool's name, a summary, a live timer and the status; it opens to the arguments and the result with copy
buttons; it opens by itself on an error and has Retry; a group sums several calls ("Used 3 tools").

## What Hypomnema already took from affectUI

On 2026-11-09 the CSS of the orchestration screen was read for the status colours, and the five status tokens in
`web/src/index.css` followed ([web stack note](2026-11-09-directories-sessions-web-stack.md)).

## How these sources are used

- affectUI's pages are a checklist of states, keys and "when not to use" for a component being designed. They are
  not the specification: there is no code for the Pro part and no server behind any of it. The specification stays
  the code of the products already studied, then AI Elements and assistant-ui, whose code can be read.
- Its components are not installed: each carries its own CSS, and the web client builds from
  `web/src/components/ui` with tokens ([conventions](../conventions.md#web-client)).
- A source without code is looked at in pictures of its real pages, a rule added to `AGENTS.md` with this note
  (owner, 2026-11-11).

## The web client's Run screen beside it

Seen in the pictures of [issue #131](https://github.com/kl3inIT/hypomnema/issues/131) (`.tmp/ui-check/131`,
2026-11-10); the code of the Run, Board and chat screens had not changed since. This is the agent's reading, not a
decision.

- One state has four wordings: "Needs you" (the list of Runs), "The worker waits for you" and "Waiting for you"
  (a Run), "Waits for you" (the Board). A Run shows `succeeded`, `pending` and `running` as stored; the Board says
  "Done" and "Working".
- A Task's status on a Run is a coloured word without an icon; the decision of 2026-11-09 is an icon and a word.
- A Task on a Run shows its name and status. The Board's card also shows the machine and the running command.
- A pending Task on a Run does not say what it waits for; the Board's card does.
- A Run shows no elapsed time, cost or tokens for the whole.
- "Finish Run" and "Cancel" are side by side, and "Cancel" does not say it cancels the Run.
- The list of Runs is plain rows, not the cards of the Machines page.

Not looked at: the chat screen (no picture, no scenario in `web/tests`) and the Agents screen.
