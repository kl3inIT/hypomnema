-- A Bot's main chat (issue #471): a stable pointer from the Bot to one of its chats, the one a click on the Bot opens
-- and the web and the CLI write to, as OpenClaw's main session per agent (src/routing/session-key.ts) and Hermes
-- Desktop's Bot Chat. Every other chat keeps its id and its whole lifecycle; it is only no longer the Bot's row.
-- NULL until the first message from the web or the CLI makes one; a deleted main chat clears it.
--
-- The main chat of a Bot that has chats is chosen among its candidates: not a Lark chat (no row in channel_chats),
-- not a Lark group's chat (channel_group, which a group's later chats keep after a reset), not the chat of someone
-- other than the owner (a person message with a sender, migration 00087), and not a Routine's chat (named by a run,
-- or opened with a Routine's prompt, which runs pruned later leave behind). The candidate with the owner's latest
-- message wins, then the one active latest, then the greatest id. No chat's id or content changes.

-- +goose Up
ALTER TABLE bots ADD COLUMN main_chat TEXT REFERENCES chats (id);

UPDATE bots SET main_chat = (
    SELECT c.id FROM chats c
    WHERE c.bot_id = bots.id
      AND c.channel_group = 0
      AND NOT EXISTS (SELECT 1 FROM channel_chats cc WHERE cc.chat_id = c.id)
      AND NOT EXISTS (SELECT 1 FROM routine_runs rr WHERE rr.chat_id = c.id)
      AND NOT EXISTS (
            SELECT 1 FROM chat_messages m
            WHERE m.chat_id = c.id AND m.kind = 'wake' AND m.event_seq IS NULL AND m.to_coordinator = 1
              AND m.text LIKE 'Routine %'
        )
      AND NOT EXISTS (
            SELECT 1 FROM chat_messages m WHERE m.chat_id = c.id AND m.role = 'owner' AND m.sender_id IS NOT NULL
        )
    ORDER BY coalesce((
            SELECT max(m.created_at) FROM chat_messages m
            WHERE m.chat_id = c.id AND m.role = 'owner' AND m.sender_id IS NULL
        ), -1) DESC,
        c.updated_at DESC, c.id DESC
    LIMIT 1
);

-- +goose Down
ALTER TABLE bots DROP COLUMN main_chat;
