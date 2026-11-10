-- Chats with the coordinator on the server (issue #41, PR 3): the owner talks to pi in a chat; the control plane keeps
-- the messages (pi's own session file is its memory, not the record) and the Runs pi created from each chat.
-- After Omnara (docs/research/2026-11-09-chat-storage.md): delivery to pi is marked per message, a wake-up is one more
-- message in the same queue, unique per event, and whether pi is working is known from its process, not stored.

-- +goose Up
CREATE TABLE chats (
    id         TEXT    NOT NULL PRIMARY KEY,
    title      TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE chat_messages (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id      TEXT    NOT NULL REFERENCES chats (id),
    -- owner: typed by the owner; coordinator: pi's reply; system: a wake-up, a tool pi ran, or a notice.
    role         TEXT    NOT NULL CHECK (role IN ('owner', 'coordinator', 'system')),
    text         TEXT    NOT NULL,
    -- A wake-up names the event it reports; one event wakes a chat once.
    event_seq    INTEGER,
    -- Owner messages and wake-ups wait for pi until delivered; other messages are never delivered.
    delivered_at INTEGER,
    created_at   INTEGER NOT NULL,
    UNIQUE (chat_id, event_seq)
) STRICT;

CREATE INDEX chat_messages_chat ON chat_messages (chat_id, seq);

-- The chat whose coordinator created the Run; NULL for Runs from the owner or an outside coordinator.
ALTER TABLE runs ADD COLUMN chat_id TEXT REFERENCES chats (id);

CREATE INDEX runs_chat ON runs (chat_id) WHERE chat_id IS NOT NULL;

-- +goose Down
DROP INDEX runs_chat;
ALTER TABLE runs DROP COLUMN chat_id;
DROP INDEX chat_messages_chat;
DROP TABLE chat_messages;
DROP TABLE chats;
