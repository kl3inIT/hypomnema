-- The chat_search index folds diacritics (issue #1218, row 3): FTS5's unicode61 tokenizer with remove_diacritics 2
-- (sqlite.org/fts5.html, "Unicode61 Tokenizer"), so that "chuyen", "noi" and "hue" find "Chuyển khoản… Hà Nội…
-- Huế", as the owner often types Vietnamese without its marks. The query is tokenized the same way, so a marked word
-- still finds its row. A tokenizer is fixed when an FTS5 table is made: the index is made again with every row the
-- chat_search migration's triggers keep in it (an owner's or a person's message and the Bot's reply by their text, a
-- wake-up by its recall words), and its triggers with it, as they were.

-- +goose Up
DROP TRIGGER chat_messages_fts_update;
DROP TRIGGER chat_messages_fts_delete;
DROP TRIGGER chat_messages_fts_insert;
DROP TABLE chat_messages_fts;

CREATE VIRTUAL TABLE chat_messages_fts USING fts5(text, tokenize = 'unicode61 remove_diacritics 2');

INSERT INTO chat_messages_fts (rowid, text)
SELECT seq, CASE WHEN role IN ('owner', 'coordinator') THEN text ELSE recall END FROM chat_messages
WHERE trim(coalesce(CASE WHEN role IN ('owner', 'coordinator') THEN text ELSE recall END, '')) <> '';

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

CREATE VIRTUAL TABLE chat_messages_fts USING fts5(text);

INSERT INTO chat_messages_fts (rowid, text)
SELECT seq, CASE WHEN role IN ('owner', 'coordinator') THEN text ELSE recall END FROM chat_messages
WHERE trim(coalesce(CASE WHEN role IN ('owner', 'coordinator') THEN text ELSE recall END, '')) <> '';

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
