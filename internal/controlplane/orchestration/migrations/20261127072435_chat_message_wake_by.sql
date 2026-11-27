-- +goose Up
-- Who an Inbox item's wake-up comes from (issue #1229): the owner's own words (his prompt when he hands the item over,
-- his comment), the owner giving it without a word, a Bot that passed it on the owner's line (the mark that makes
-- the item's chat his turn for his own accounts), or a Bot otherwise. wake_todo is the item it wakes for: a mark is
-- that item's only.
ALTER TABLE chat_messages ADD COLUMN wake_by TEXT
    CHECK (wake_by IS NULL OR wake_by IN ('owner', 'owner_gave', 'bot_for_owner', 'bot'));
ALTER TABLE chat_messages ADD COLUMN wake_todo TEXT;
CREATE INDEX chat_messages_wake_by ON chat_messages (chat_id) WHERE wake_by IS NOT NULL;

-- The wake-ups of before in the items' chats, the only chats an item's wake-up goes to again: one that carries the
-- owner's words (recall, which only his prompt and his comment set) is his; any other is a Bot's, with nothing to
-- show it was on his line, so that chat takes no mark.
UPDATE chat_messages SET wake_by = CASE WHEN recall IS NOT NULL THEN 'owner' ELSE 'bot' END
WHERE role = 'system' AND kind = 'wake' AND event_seq IS NULL AND to_coordinator = 1
  AND chat_id IN (SELECT chat_id FROM todos WHERE chat_id IS NOT NULL);

-- +goose Down
DROP INDEX chat_messages_wake_by;
ALTER TABLE chat_messages DROP COLUMN wake_todo;
ALTER TABLE chat_messages DROP COLUMN wake_by;
