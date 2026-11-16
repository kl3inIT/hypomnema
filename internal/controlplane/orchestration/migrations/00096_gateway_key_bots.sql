-- A chat's gateway key names the Bot of its chat (issue #626), so the owner's usage is told by Bot even after the
-- chat is deleted, which takes the chat from its keys (issue #484). A key of a chat still there is given its Bot.

-- +goose Up
ALTER TABLE gateway_keys ADD COLUMN bot_id TEXT;
UPDATE gateway_keys SET bot_id = (SELECT c.bot_id FROM chats c WHERE c.id = gateway_keys.chat_id)
WHERE chat_id IS NOT NULL;

-- +goose Down
ALTER TABLE gateway_keys DROP COLUMN bot_id;
