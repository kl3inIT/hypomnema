# Where one objective carried out by several agents is seen (2026-11-11)

Read after the owner asked whether the web client's Run page repeats the Board, and asked for a proposal without
bias. The question for each reference: when one objective is split among several agents, where does the user see
it as a whole, and what does that view show. Nothing is decided here.

Three sub-agents read the code; a line marked "(agent)" was not opened again. The lines checked again by hand are
marked "(checked)". Revisions: Orca `4c13ae46`, genie `68bf387`, Omnara `0ef1711`, Omnigent `cf1021e`, OpenMausBot
`1de1164`, Coder `b798061`, t3code `b8c2cfad`, all in `.tmp/`.

## The references

| Project | The unit | A page of its own for one unit | Where the whole is seen | Totals for the whole | Stopping the whole |
|---|---|---|---|---|---|
| Orca | Run, with Tasks and their `deps` (`src/main/runtime/orchestration/db/schema`) | None in the app; `orca orchestration run-show` and `task-list` in the CLI (agent) | Per agent: workers nested under their coordinator in the sidebar, and the experimental Agents kanban. A panel of a coordinator's crew exists and is switched off (`orchestration-crew/use-orchestration-panel-enabled.ts`, checked) | None; a Run has no status (agent) | None for a Run; `worker-stop` per worker (agent) |
| genie | Wish, a slug on the board's cards | None | The board filtered to one wish, `genie board --wish <slug>` (`src/term-commands/v5-board.ts`, checked); the detail is per card: status, claimant, "Depends on" as tags, comments, history (`plugins/dsh-genie-board/src/client/BoardPanel.tsx`, agent) | After the run, in the CLI: minutes, tokens, tool calls, agents, and a row per stage with its model (`src/term-commands/wish.ts`, agent) | None |
| Omnara | Agent, children by `parent_agent_id` | None | The parent agent's page: a "Subagents" group in its right sidebar, one row a child with its name, key and state (`components/agents/AgentSidebar.tsx`, checked). The list of agents hides subagents unless a filter is on | Cost, tokens in and out, calls, per model, with subagents and a switch "Exclude subagents" (same file, checked). No elapsed time, no n of m | "Stop" in the parent's composer; "Archive" (agent) |
| Omnigent | Session, children by `parentSessionId` | None | An "Agents" tab of the root session's side panel: an indented tree, one row a child with a status tile, the harness's icon, a label, the model and the last message; a switch to a graph (`shell/SubagentsPanel.tsx`, checked; `SubagentsGraphView.tsx`, agent) | A badge working/total on the tab; the session's cost and tokens over its subtree in a popover (agent) | "Stop session" on the session (agent) |
| OpenMausBot | Assignment on the server; a thread in the UI | None | The chat of the bot that handed the work on, as chips; child threads under "Opened by bots" in the sidebar. A trace of one run (a tree of turns with bars on a time axis) is on the Usage page, for admins (`usage/TraceWaterfall.tsx`, agent) | In that trace: Duration, Cost, Tokens, AI steps, Tool uses, Errors (agent) | "Stop" in the composer, which reaches the tree (`docs/runtime-contract.md`, agent) |
| Coder | Chat, children by `parent_chat_id`, one level | None | The root chat: children indented under it in the sidebar, closed by default; a subagent is a tool row in the parent's transcript (agent) | One cost for the whole tree (`AgentsPage/components/ChatSummary.tsx`, checked) | "Archive agent" at the root, which reaches the children; "Stop" for the open chat (agent) |
| t3code | Thread, subagents by `parentThreadId` | None | A "Lineage" section in the parent thread's details: a row a child with the provider's icon, title and running time; a workflow opens to phases with "n/m agents" (`chat/ThreadRelationshipsControl.tsx`, `ThreadLineageWorkflowRow.tsx`, checked) | "N running", "N failed", "n/m agents"; no cost or tokens per thread (agent) | "Stop entire workflow", "Stop subagent" (checked) |

What they share:

- None has a page for one objective apart from the parent's own view. Where the parent is a chat (Omnara,
  Omnigent, Coder, t3code, OpenMausBot), the children are a list or an indented tree in a panel beside that chat.
  Where the coordinator is an agent in a terminal (Orca) or a CLI (genie), the whole is the board, per agent or
  filtered to the unit.
- A child's row: a status, the runtime's or provider's icon, a title; the model in Omnigent, Coder (tool row) and
  t3code (tooltip); the running time in t3code and OpenMausBot.
- Children relate by indentation. A graph is a second view in Omnigent only. Dependencies between siblings are
  tags on a card in genie and raw ids in Orca's CLI; no one draws them.
- A child's question comes up in the parent's view: above the parent's composer with "From subagent" (Omnara),
  as a card "Requested by sub-agent" (Omnigent), "Approval needed from" (t3code).
- A cost for the whole, children included, in four (Omnara, Omnigent, Coder, OpenMausBot). A time axis with bars
  in one (OpenMausBot), on a usage page.
- Finished units are found in the same list, by a filter or a group (Archived, Settled, Done), not on another page.

## Hypomnema today

Read in `web/src/features` and `web/src/routes`.

- The Board (`/board`) shows every open Task as a card, in columns by status, with Group by Status, Run or
  repository; Done holds 24 hours. A card opens the Task panel, where a question is answered.
- The list of Runs (`/runs`) groups Runs into Needs you, Open, Finished, Canceled.
- A Run's page (`/runs/$runId`) shows the objective, the repository, a link to the chat that made it, the open
  questions with the same form, the Tasks as rows (number, first line, pull request, the stored status), the
  answered questions, and "Finish Run" and "Cancel".
- A chat with pi does not show the Runs it made (no link to a Run in `features/chat`).
- A Run may have no chat: its coordinator can be the owner's own agent calling the Client API
  ([glossary](../glossary.md)), which is Orca's and genie's case.

A Run's page is the one screen here that no reference has. What only it holds: the two controls on the Run, the
answered questions, and Tasks older than 24 hours.
