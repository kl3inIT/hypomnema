-- +goose Up
ALTER TABLE routines ADD COLUMN access TEXT NOT NULL DEFAULT 'full' CHECK (access IN ('full', 'read'));
ALTER TABLE chat_messages ADD COLUMN read_run INTEGER NOT NULL DEFAULT 0 CHECK (read_run IN (0, 1));
CREATE INDEX chat_messages_read_run ON chat_messages (chat_id) WHERE read_run = 1;

-- +goose Down
DROP INDEX chat_messages_read_run;
ALTER TABLE chat_messages DROP COLUMN read_run;
ALTER TABLE routines DROP COLUMN access;
