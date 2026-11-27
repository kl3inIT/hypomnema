-- +goose Up
-- Who an Inbox item's wake-up comes from (issue #1229): the owner (his hand-over or comment), a Bot that passed it on
-- the owner's line (the mark that makes the item's chat his turn for his own accounts), or a Bot otherwise.
ALTER TABLE chat_messages ADD COLUMN wake_by TEXT CHECK (wake_by IS NULL OR wake_by IN ('owner', 'bot_for_owner', 'bot'));
CREATE INDEX chat_messages_wake_by ON chat_messages (chat_id) WHERE wake_by IS NOT NULL;

-- +goose Down
DROP INDEX chat_messages_wake_by;
ALTER TABLE chat_messages DROP COLUMN wake_by;
