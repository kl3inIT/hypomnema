-- A Bot searches its own past chats by words and recalls them when the owner writes (issue #805), over SQLite's FTS5
-- as Hermes searches its sessions (hermes_state.py, messages_fts) and OpenMausBot its threads (server/message-db.ts,
-- messages_fts), and as this repository's Zalo mirror searches its messages (messaging/zalo/store.go).
--
-- What is indexed is what was said: an owner's or a person's message and the Bot's reply, by their text; and a
-- wake-up of the control plane that carries the owner's own words, an Inbox item handed with his prompt or his
-- comment on it, by those words (recall). A tool's line, a Run's wake-up and the other notices are not. The index
-- keeps its own copy of the text (zl_messages_fts's shape) since a wake-up's indexed words are not its text; its
-- rowid is the message's seq. The triggers keep it as the rows are written, changed and deleted; the chats kept
-- before this are indexed here once. A later rebuild of chat_messages has to make the triggers again.
--
-- recall is the words a wake-up carries that the turn it starts is recalled by: the owner's comment, or the item's
-- title and the owner's prompt for a hand-over; NULL for every other row, which is recalled by its text when it is
-- the owner's (coordinator/recall.go).

-- +goose Up
ALTER TABLE chat_messages ADD COLUMN recall TEXT;

CREATE VIRTUAL TABLE chat_messages_fts USING fts5(text);

INSERT INTO chat_messages_fts (rowid, text)
SELECT seq, text FROM chat_messages WHERE role IN ('owner', 'coordinator') AND trim(text) <> '';

-- +goose StatementBegin
CREATE TRIGGER chat_messages_fts_insert AFTER INSERT ON chat_messages BEGIN
    INSERT INTO chat_messages_fts (rowid, text)
    SELECT new.seq, CASE WHEN new.role IN ('owner', 'coordinator') THEN new.text ELSE new.recall END
    WHERE trim(coalesce(CASE WHEN new.role IN ('owner', 'coordinator') THEN new.text ELSE new.recall END, '')) <> '';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_messages_fts_delete AFTER DELETE ON chat_messages BEGIN
    DELETE FROM chat_messages_fts WHERE rowid = old.seq;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER chat_messages_fts_update AFTER UPDATE OF role, text, recall ON chat_messages BEGIN
    DELETE FROM chat_messages_fts WHERE rowid = old.seq;
    INSERT INTO chat_messages_fts (rowid, text)
    SELECT new.seq, CASE WHEN new.role IN ('owner', 'coordinator') THEN new.text ELSE new.recall END
    WHERE trim(coalesce(CASE WHEN new.role IN ('owner', 'coordinator') THEN new.text ELSE new.recall END, '')) <> '';
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER chat_messages_fts_update;
DROP TRIGGER chat_messages_fts_delete;
DROP TRIGGER chat_messages_fts_insert;
DROP TABLE chat_messages_fts;
ALTER TABLE chat_messages DROP COLUMN recall;
