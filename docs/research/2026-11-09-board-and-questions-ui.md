# The board of work in flight, and answering questions on the web (2026-11-09)

What the references show for many agents at once, and for a question an agent puts to a person, read for issue #41
PR 5 (the web's Runs and questions). Revisions read: t3code `b8c2cfad`, Omnara `0ef1711`, Omnigent `cf1021e`, Orca
`4c13ae46`, OpenMausBot `1de1164`.

## Many agents at once

| Project | Default view | Other views | Read |
|---|---|---|---|
| Orca | Worktree list in the sidebar | An agent kanban, behind an experimental setting: columns Needs You, Working, Done, Idle (by status, newest change first, no drag and drop). A card shows the agent's icon, its conversation, the last message of each side, subagents, the host, the worktree, the pull request and its age. A card that asks is tinted and shows the question; a click opens the agent's terminal. No phone layout (columns scroll sideways). | `src/renderer/src/components/dashboard-popout/AgentKanbanBoard.tsx`, `AgentKanbanCard.tsx`, `components/dashboard/dashboard-card-bucket.ts`, `AgentDashboardExperimentalSetting.tsx` |
| Omnigent | Sessions in the sidebar; an Inbox of what waits for the user | Canvas (behind the `canvas` feature): a card per session on a lattice, one canvas per project, placed by dragging; positions kept in the browser per server and user; a click opens the session over the canvas. Grid (behind `grid_sessions_display_ui`): several live sessions side by side. | `web/src/canvas/CanvasWorkspace.tsx`, `SessionCard.tsx`, `canvasStorage.ts`, `web/src/grid/GridWorkspace.tsx`, `web/src/App.tsx` |
| OpenMausBot | Chat with a contact list | Team map: a pan-and-zoom canvas of bots by department; a card has the bot's mascot, a status dot and word, and its provider's mark in the model chip; computers drawn as live screens joined to their bots. An org chart by person. | `src/components/TeamMapPage.tsx`, `TeamCanvas.tsx`, `ProviderIcons.tsx`, `Avatar.tsx` |
| Omnara | A card list of agents (Working, Waiting, Idle) with search, sort and filters | None | `frontend/apps/web/src/components/agents/AgentsSection.tsx`, `AgentCardList.tsx`, `lib/agent-status.ts` |
| t3code | Threads in the sidebar, a status pill each (Pending Approval, Awaiting Input, Working, Failed, Completed, by priority) | A pull request list in groups with counts | `components/Sidebar.logic.ts`, `ThreadStatusIndicators.tsx`, `pullRequest/PullRequestListGroups.tsx` |

None draws a dependency graph between tasks. No board has drag and drop between status columns: the agents change
their own status.

## Questions to a person

| Project | Where | How it is answered | After |
|---|---|---|---|
| t3code | Docked above the composer; one at a time with i/N | Options as radio rows, 1 to 9 pick, the composer is the free-text box | A work row "Answered questions" in the timeline |
| Omnara | Docked above the composer, for the agent and its subagents | A form per question: option buttons, a text field where the option allows it, submit | Removed from the dock |
| Omnigent | Inline in the chat, in a tray above the composer, and in the Inbox | Radio options, free text as one more option, Back/Next/Submit | A resolved card with the answer |
| Orca | A flag on the worker's row that sorts it into Needs You | Text only: a message to the worker | |

No reference answers a question from a card in a list; each opens it where the whole question and its context
show.

## Decided by the owner (2026-11-09)

- A board by status, after Orca, with Group by Status, Run or repository and a repository filter. A free canvas
  (Omnigent, OpenMausBot) was compared in a prototype and left out: workers live for one Task, so places on a
  canvas would keep changing, what waits for the owner is not first, and a canvas is hard to use on a phone.
- The runtime's mark leads a card (OpenMausBot's provider marks), not a mascot: a worker has no lasting identity.
- A card opens the Task over the board (Omnigent), where the question is answered as in t3code's panel.
- What the worker does now and its session wait for worker sessions on the server (#43); until then a card shows
  how long it has run and its last sign of life. A pull request's state (merged, CI) is not read from GitHub yet.

Inferred, with no reference:
- Done holds the last 24 hours; older work is on the Runs page.
- `GET /tasks` returns the newest event's seq, so the page follows the event stream from its own snapshot without
  replaying history.
