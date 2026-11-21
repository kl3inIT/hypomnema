-- The commands a chat's pi runs in the background (issue #1058), one row each while it runs, as OpenClaw records a
-- wake-armed background command while it runs and removes the row when it exits (src/agents/
-- bash-process-durable.ts:83-129). A command that ends takes its row out and leaves a wake-up in its chat; one whose
-- pi stopped first, or that a restart of the control plane cut, is reported lost from its row, as OpenClaw reports a
-- command the last Gateway lost (bash-process-durable.ts:132-161). No command text is kept: it may hold
-- credentials, and the chat's history has it (bash-process-durable.ts:110).
-- - id: the command's id, as the pi extension that ran it named it.
-- - pi: the hash of the token of the pi that ran it, which ends with that pi; a pi that stops loses its rows only.
-- - started_at: when it started (Unix milliseconds).

-- +goose Up
CREATE TABLE chat_background_commands (
    chat_id    TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    id         TEXT    NOT NULL,
    pi         TEXT    NOT NULL,
    started_at INTEGER NOT NULL,
    PRIMARY KEY (chat_id, id)
);

-- +goose Down
DROP TABLE chat_background_commands;
