# OpenMausBot's job runtime and team map, and a canvas for the web (2026-11-12)

The owner asked on 2026-11-12 whether the Board could become a canvas "as n8n and OpenMausBot have", and what
OpenMausBot's orchestration has worth learning. Revision read: OpenMausBot `1de1164`. Nothing here is decided; the
last section lists what waits for the owner.

What was read: `docs/runtime-contract.md` (the whole page), the head of `src/components/TeamCanvas.tsx`, the first
part of `docs/superpowers/plans/2026-08-31-07-delegation-task-board.md`, and file lists. **Not read:** the code of
`server/runtime/` that the contract says implements it, so each rule below is what the contract states, not what
was seen to run. n8n has no clone in `.tmp/` and was not studied; its pages were not photographed.

## What its canvas is

- `TeamCanvas.tsx` draws who exists, not work: a card per bot in its department, a computer joined to its bots by a
  line (`polylinePath`, an SVG per team tile), dragging a bot between teams. Pan and zoom are written by hand with
  pointer and wheel events and one CSS transform (L236-250, L411); no canvas library.
- Work one bot hands to another is not drawn as a graph. It is a tree of records (below) shown as cards in the
  chats; the plan that first made delegated work visible calls its screen a board "with owners, status, and
  deadlines".
- The other references that draw a canvas use `@xyflow/react` 12: Omnigent (`web/package.json`) and AI Elements
  (`packages/elements`, with `canvas`, `node`, `edge`, `connection`, `controls`, `panel`, `toolbar`).
- The earlier comparison and the owner's decision of 2026-11-09 (a board, not a canvas) are in
  [2026-11-09-board-and-questions-ui.md](2026-11-09-board-and-questions-ui.md). Two things changed since: a Bot is
  now a lasting record (issue #169), which is what a team map draws, and a Run's Tasks depend on each other, which
  is what a flow canvas draws.

## The job runtime, as its contract states it

One record (`Assignment`), one lifecycle and one wake lane for every piece of work a bot hands on, whatever started
it. The page says it replaced "two delegation systems, six wake lanes, a dozen clocks and ten trust wordings".

| Rule | Where in the contract | Hypomnema today (read in code) |
|---|---|---|
| A tree: `rootId`, `parentId`, `depth`; `origin` (`person`, `routine`, `outside`) written once at the root and copied down | §1 | Run, Task, Dispatch; no origin on a Run |
| A turn cut by a restart runs again once; not at all when the host knows it already acted outward (sent, posted, pushed, paid), read from the thread's activity rows, never from the model's text | §5, §8 `outward` | `dispatch_effects` holds intent and done per Dispatch (`migrations/00002_dispatches.sql` L138); a lost lease makes the outcome `unknown`, which blocks a replacement until the coordinator settles it, and "effects are never replayed automatically" (`dispatch_types.go` L239). `RecordEffect` is called only from the node's reports (`nodeapi/reports.go` L100) |
| A node that continues stops as `stalled` after two continuations with no successful tool call, or the same error three times | §2, §6 | One timer: no progress for 15 minutes (`limits.go` `StallAfter`) |
| The opening of a handed-on turn is written by the harness from stored data: `[From <who> · <what> · <authority>]`, authority being "on their behalf at your level" for work a person or their routine started and "information only" for everything else; an imitation of the note in other text is escaped | §4 | Not found by a search for such wording |
| Work whose origin is outside (webhook, relay, guest) never inherits a person's approval level | §3 | No origin, so no such rule |
| A child's question wakes the waiting parent at once; at full access the requester answers it, or the child's named default stands | §2 | A worker's question goes to the owner or the coordinator (`questions.go`) |
| The same request to the same bot from two requesters is answered once | §2 | Not found |
| Three budgets per root (requests, executions, wakes), renewed by progress; three clocks and no others | §6 | Failure counts and timers a Dispatch; no budget a Run (issue #22) |
| Ten invariants asserted after every scenario test, such as "no result is delivered twice" and "every status change has an event" | §7 | Not compared |
| Every turn of a conversation is listed the same tools, byte for byte, so the provider's cached prompt carries over; a tool a turn may not use answers with the reason | §9 | Not compared |

Inferred, not read:
- The service proxy's log of a write that passed (`event=serviceproxy.request`) is not written as a
  `dispatch_effects` row, since only the node's reports call `RecordEffect`. A sent mail is therefore not among the
  effects a coordinator checks before settling an unknown Dispatch.

## Proposed to the owner, not decided

1. Keep the Board. Add a team map as its own page (pi, the Bots, the machines), after OpenMausBot, drawn with
   `@xyflow/react` as Omnigent and AI Elements do. First a prototype from pictures of OpenMausBot's real page.
2. Two rules of the runtime as issues of their own: the loop guard read from evidence, and the harness-written
   opening with origin and authority (for the Lark Channel and several Bots, issue #169).
3. The child's question (for issue #55) and the budgets (for issue #22) are noted on those issues.
