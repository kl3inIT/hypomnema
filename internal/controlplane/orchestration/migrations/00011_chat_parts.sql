-- Chats keep pi's whole activity (issue #41, PR 4), as Omnara does: a reply keeps its text, thinking and tool calls in
-- parts, and each finished tool call is a message of its own with its result. The live text between them is never
-- stored; the stream relays it from memory. SQLite changes a CHECK only by rebuilding the table.

-- +goose Up
CREATE TABLE chat_messages_new (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id      TEXT    NOT NULL REFERENCES chats (id),
    -- owner: typed by the owner; coordinator: pi's reply; tool: a tool pi ran, with its result; system: a wake-up or
    -- a notice.
    role         TEXT    NOT NULL CHECK (role IN ('owner', 'coordinator', 'tool', 'system')),
    -- The plain text: the reply, or one line saying what ran; what the CLI prints.
    text         TEXT    NOT NULL,
    -- A JSON array of parts (text, thinking, tool_call, tool_result) for coordinator and tool messages.
    parts        TEXT CHECK (parts IS NULL OR json_valid(parts)),
    -- A wake-up names the event it reports; one event wakes a chat once.
    event_seq    INTEGER,
    -- Owner messages and wake-ups wait for pi until delivered; other messages are never delivered.
    delivered_at INTEGER,
    created_at   INTEGER NOT NULL,
    UNIQUE (chat_id, event_seq)
) STRICT;

INSERT INTO chat_messages_new (seq, chat_id, role, text, event_seq, delivered_at, created_at)
SELECT seq, chat_id, role, text, event_seq, delivered_at, created_at FROM chat_messages;

DROP INDEX chat_messages_chat;
DROP TABLE chat_messages;
ALTER TABLE chat_messages_new RENAME TO chat_messages;
CREATE INDEX chat_messages_chat ON chat_messages (chat_id, seq);

-- +goose Down
CREATE TABLE chat_messages_old (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id      TEXT    NOT NULL REFERENCES chats (id),
    role         TEXT    NOT NULL CHECK (role IN ('owner', 'coordinator', 'system')),
    text         TEXT    NOT NULL,
    event_seq    INTEGER,
    delivered_at INTEGER,
    created_at   INTEGER NOT NULL,
    UNIQUE (chat_id, event_seq)
) STRICT;

INSERT INTO chat_messages_old (seq, chat_id, role, text, event_seq, delivered_at, created_at)
SELECT seq, chat_id, CASE role WHEN 'tool' THEN 'system' ELSE role END, text, event_seq, delivered_at, created_at
FROM chat_messages;

DROP INDEX chat_messages_chat;
DROP TABLE chat_messages;
ALTER TABLE chat_messages_old RENAME TO chat_messages;
CREATE INDEX chat_messages_chat ON chat_messages (chat_id, seq);
