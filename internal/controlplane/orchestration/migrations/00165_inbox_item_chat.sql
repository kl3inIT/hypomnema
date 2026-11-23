-- An Inbox item's own chat with the Bot it is given to (issue #1111): handing an item to a Bot opens a chat of that
-- Bot for the item, as Orca's bot item keeps the chat it belongs to (src/main/bots/bot-items-schema.ts, items.chat_id),
-- and wakes the Bot there with the item; the owner's later comments wake it in the same chat. NULL until the item is
-- first given to a Bot, and again once that chat is deleted (deleteChatTx clears it in the deletion's transaction).

-- +goose Up
ALTER TABLE todos ADD COLUMN chat_id TEXT;

CREATE INDEX todos_chat ON todos (chat_id) WHERE chat_id IS NOT NULL;

-- +goose Down
DROP INDEX todos_chat;

ALTER TABLE todos DROP COLUMN chat_id;
