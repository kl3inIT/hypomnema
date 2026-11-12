-- What a system row of a chat is (issue #267, the design's item 0 in #254), as t3code types its timeline items
-- (MessagesTimeline.logic.ts) and OpenMausBot names its stop rows: a wake-up, a failed model call, a turn the owner
-- stopped, or an answered Approval. Owner, coordinator and tool rows have none. Rows kept before this are told
-- apart as the readers did until now: a wake-up names its event, an Approval's notice is for the coordinator, and a
-- failure's text is the coordinator's (coordinator/activity.go).

-- +goose Up
ALTER TABLE chat_messages ADD COLUMN kind TEXT NOT NULL DEFAULT ''
    CHECK (kind IN ('', 'wake', 'failed', 'stopped', 'approval'));

UPDATE chat_messages SET kind = 'wake' WHERE role = 'system' AND event_seq IS NOT NULL;
UPDATE chat_messages SET kind = 'approval' WHERE role = 'system' AND kind = '' AND to_coordinator = 1;
UPDATE chat_messages SET kind = 'failed'
WHERE role = 'system' AND kind = '' AND text LIKE 'pi''s model call failed%';

-- +goose Down
ALTER TABLE chat_messages DROP COLUMN kind;
