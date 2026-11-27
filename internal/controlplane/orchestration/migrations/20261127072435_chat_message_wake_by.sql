-- +goose Up
-- Who an Inbox item's wake-up comes from (issue #1229): the owner's own words (his prompt when he hands the item over,
-- his comment), the owner giving it without a word, a Bot that passed it on the owner's line (the mark that makes
-- the item's chat his turn for his own accounts), or a Bot otherwise. wake_todo is the item it wakes for: a mark is
-- that item's only.
ALTER TABLE chat_messages ADD COLUMN wake_by TEXT
    CHECK (wake_by IS NULL OR wake_by IN ('owner', 'owner_gave', 'bot_for_owner', 'bot'));
ALTER TABLE chat_messages ADD COLUMN wake_todo TEXT;

-- The items' wake-ups of before that carry the owner's words (recall, which only his prompt and his comment set) are
-- his; any other stays unmarked, which LastTurnInput takes as an input that clears a mark.
UPDATE chat_messages SET wake_by = 'owner'
WHERE recall IS NOT NULL AND role = 'system' AND kind = 'wake' AND event_seq IS NULL;

-- +goose Down
ALTER TABLE chat_messages DROP COLUMN wake_todo;
ALTER TABLE chat_messages DROP COLUMN wake_by;
