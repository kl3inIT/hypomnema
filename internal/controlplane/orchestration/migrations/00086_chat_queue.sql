-- Owner messages written while the chat's pi works wait here, off the transcript, until the turn settles (issue
-- #497, item 2 of #254): OpenMausBot keeps follow-ups off the transcript until it settles
-- (server/channel-queue.ts), so the reply that runs cannot seem to answer words it never saw, and the owner can
-- remove one or send it now (Omnigent QueuedMessagesStrip.tsx, t3code QueuedRunsControl.tsx). The supervisor moves
-- a chat's rows into chat_messages, in order, when it gives them to pi.

-- +goose Up
CREATE TABLE chat_queue (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id     TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    text        TEXT    NOT NULL,
    -- A JSON array of the files attached, as chat_messages.attachments.
    attachments TEXT,
    created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX chat_queue_chat ON chat_queue (chat_id, id);

-- +goose Down
DROP TABLE chat_queue;
