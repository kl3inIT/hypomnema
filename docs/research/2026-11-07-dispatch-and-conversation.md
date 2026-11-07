# Dispatch and Conversation: continuing a worker for follow-up work

Question: when a Task needs more work after its first attempt (a review finding, a red CI run, a message from the
owner), does the next attempt start a fresh worker or continue the one that did the work? Until now a Dispatch was
one attempt and nothing recorded the worker's CLI conversation, so only a fresh start was possible.

Read on 2026-11-07, from the clones in `.tmp/`:

| Reference | How | Where |
|---|---|---|
| OpenClaw (`d622a2b`) | A run and a session are separate: "A subagent run ends; a session does not." Sub-agents are hidden by default and announce to the parent; a persistent visible session is created only when the user wants to return to and steer that work | `docs/tools/subagents.md` |
| Omnigent (`7230afa`) | A child is a session. The manager continues it with `sys_session_send`; each send is tracked child work whose completion returns to the manager's inbox. `sys_session_message` is a plain message to a session that is not a child | `docs/session-messaging-tool-boundary.md` |
| Orca (`4c13ae46`) | Each attempt is a Dispatch. A new Dispatch may reuse a proven agent whose previous work is settled (`worker-start --terminal <handle>` in the same worktree); an unknown or still-busy predecessor is never reused | `skill-guides/orchestration/references/recovery-and-cleanup.md` |

Common shape: an attempt ends and has an outcome; the conversation outlives it and a later attempt can continue it.

Decision (owner, 2026-11-07):
- A **Dispatch** stays one attempt (epoch, node, account, vault session, outcome).
- A **Conversation** records the worker's CLI conversation (Claude Code session id, Codex thread id) with its Node,
  worktree, runtime and account. Each Dispatch records the Conversation it used.
- A follow-up Dispatch of the same Task continues the Conversation when the previous Dispatch settled cleanly and the
  Node and worktree are still there (Orca's reuse rule). Context stays, and the pinned account keeps the prompt cache
  warm ([prompt-cache rules](2026-11-07-workers-accounts-gateway.md)).
- A crash, an unknown outcome or a lost Node starts a new Conversation with a recovery note (durable-harness), never
  a blind replay.
- Workers run hidden by default and announce to the coordinator (OpenClaw); the web chat can open a worker's
  Conversation to read it and to steer a running Dispatch through its mailbox.

Trade-offs: one more table and term; a continued Conversation is tied to its Node; long Conversations need compaction.
Inferred, not read in a reference: compaction policy for long worker Conversations.
