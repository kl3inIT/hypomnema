# How the references store a chat with an agent

Date: 2026-11-09. Question, for issue #41 PR 3 (the owner chatting with pi): how do the references store messages,
know what reached the agent, know whether it is working, wake it when delegated work reports, and link that work to
the conversation? Read in `.tmp/` (migrations, models), paths relative to it.

| Need | Omnara | OpenClaw | Orca | Omnigent |
|---|---|---|---|---|
| Messages | `agent_events` per agent with a unique `sequence` and an `event_kind` (`migrations/000004_events_messages_turns.sql:4-60`) | `transcript_events (session_id, seq)` with `message_role` (`src/state/openclaw-agent-schema.sql:454`) | `messages` with `sequence`, `from_handle`/`to_handle`, `type` (`src/main/runtime/orchestration/db/schema/create-core-tables-sql.ts:23-48`) | `conversation_items` by `position`, roles user and assistant (`omnigent/db/db_models.py:960-1062`) |
| What reached the agent | Per input: `agent_inputs.state` (received, resolved, rejected, canceled), `admitted_event_id`, an idempotency key (`000004…:91-192`) | Per input: `session_pending_inputs.state`, `consumed_event_id`, `UNIQUE(session_id, idempotency_key)` (`openclaw-agent-schema.sql:887-905`) | Per message: `delivered_at`, `read`, and `deliveries` acknowledged by the agent | A flag in memory |
| Working or idle | Derived on read from locks, wake-ups and execution state (`000068_agent_activity_state.sql:7-38`); never stored | `status` column | `coordinator_runs.status` | `live_status`, a stored mirror of the live state (`db_models.py:701`) |
| Waking on delegated results | A child's final answer becomes an input of the parent, deduplicated by an idempotency key (`internal/storage/internal/agentexecution/commands_agents.go:105-135`); `agent_wakeups` holds one row per agent | `task_delivery_state.last_notified_event_at` per task; `delivery.status` per subagent run | Delivered and acknowledged per message | In memory, deduplicated by a set |
| Work linked to the conversation | `agents.parent_agent_id` | `subagent_runs.requester_session_key` | `runs.coordinator_handle` | `conversations.parent_conversation_id` |

## Decided for Hypomnema (owner, 2026-11-09)

After Omnara:

- `chat_messages` holds every message (`owner`, `coordinator`, `system`). Owner messages and wake-ups carry
  `delivered_at`, set when pi gets them: delivery is marked per message, as all three references with an input queue do.
- A wake-up is a `system` message in the same queue, naming its event (`event_seq`, unique per chat), so one event
  wakes a chat once.
- Whether pi is working is not stored: the supervisor in the control plane knows it, and a restart of the control
  plane makes it true again (no pi runs), as Omnara derives it instead of writing a flag.
- `runs.chat_id` links a Run to the chat whose pi created it, as every reference links delegated work.
