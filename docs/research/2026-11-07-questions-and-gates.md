# Questions, Gates and where they appear (2026-11-07)

How a worker asks a person or the coordinator something mid-work, how the answer gets back, and where the question
appears in a UI. Studied for issue #19, PR 3. Revisions read:
- Orca `4c13ae46`;
- Omnara `0ef1711`;
- Coder `b798061`;
- Omnigent `cf1021e`.

## What each does

| Project | Model | Read |
|---|---|---|
| Orca | Two concepts. **Decision gate**:<br>- a worker's `decision_gate` message opens it;<br>- the active Dispatch is completed (`completeActiveDispatchesForTask`) and the Task blocked;<br>- resolving it makes the Task ready, and the next Dispatch's preamble carries "DECISION GATE RESOLVED" with the question and resolution;<br>- a `timeout` status exists, but nothing triggers it.<br><br>**Question**:<br>- a `question` message to the Run's mailbox, with a `question_threads` row (`pending`, `answered`, `closed`);<br>- the worker stays alive, and the answer is mail to `dispatch:<id>`;<br>- the first answer wins: the same body again is a duplicate, a different one is `answer_conflict`;<br>- `closeQuestionsForDispatch` runs on every settlement and stop.<br><br>**Mailbox**: messages with a monotonic `sequence`, `to_handle`, `thread_id`, `delivered_at` (prevents push replay) and `read` (explicit check); a consumer generation fences stale readers | `src/main/runtime/orchestration/db/decision-gates/decision-gate-store.ts`, `db/questions/question-threads.ts`, `db/messages/message-insert.ts`, `db/messages/message-inbox.ts`, `db/messages/mailbox-consumer.ts`, `coordinator-decision-gates.ts`, `coordinator-task-dispatch.ts:112` |
| Omnara | One concept. **Interaction**:<br>- kinds `permission` and `question`, each tied to one tool call that waits while the interaction is open;<br>- states `open`, `resolved`, `canceled`;<br>- the request is a form: title, context, questions with options, and `allows_text`;<br>- first writer wins: the same answers replay, different ones are `idempotency_key_conflict`;<br>- canceled by a stop or by new steering input;<br>- waits "as long as it takes" and survives worker restarts;<br>- a presentation receipt posts a question once per channel (Slack, Discord).<br><br>**UI**: cards at the bottom of the agent's conversation; the agents list shows `waiting_on_interaction` and `open_interactions` | `internal/storage/internal/agentexecution/commands_interactions.go`, `internal/interactionform/form.go`, `migrations/000005_model_context_tools_policy.sql` (`agent_interactions`), `docs/events/interactions.mdx`, `docs/agents/overview.mdx`, `docs/agents/failure-handling.mdx` |
| Coder | `requires_action`: the chat waits for dynamic tool results (from a machine, not a person) until a 5 min deadline, then cancels them with synthetic results. The worker holds no turn while waiting | `coderd/x/chatd/ARCHITECTURE.md` |
| Omnigent | **Elicitation**:<br>- the CLI's own permission prompts and multiple-choice questions are detected in the harness transcript and shown as cards in the session's chat;<br>- an answer from the web is typed into the CLI;<br>- an answer given in the CLI clears the card | `docs/cursor-native-elicitation.md` |

## Common ground

- No project shows questions in a separate place: a question appears in the conversation where it arose, plus a
  list of what waits for a person (Omnara).
- The first answer wins and a repeat of the same answer is harmless (Orca, Omnara).
- A pending question is closed when its attempt ends (Orca, Omnara).
- A decision that may take long ends the attempt or releases the worker (Orca's Gate, Coder's `requires_action`);
  a short question keeps the worker alive (Orca's question, Omnara's interaction).

## What Hypomnema takes

The design is in issue #19 (PR 3 comment):
- one `questions` table, as Omnara, with Orca's two behaviors as a mode (`wait` or `gate`);
- answers reach the node in the heartbeat reply;
- no general mailbox until the coordinator needs to steer workers.

Inferred, with no reference:
- pausing the stall and duration timers while a worker waits;
- reminders to the owner after 1 h, then daily;
- size limits.
