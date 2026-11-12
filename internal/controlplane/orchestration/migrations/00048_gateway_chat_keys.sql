-- +goose Up
-- A gateway key made for one chat's pi (issue #204): pi calls the gateway with it, and it is revoked when that pi
-- ends, as a Dispatch's key is when the Dispatch settles.
ALTER TABLE gateway_keys ADD COLUMN chat_id TEXT;
CREATE INDEX gateway_keys_chat ON gateway_keys (chat_id) WHERE chat_id IS NOT NULL;

-- +goose Down
DROP INDEX gateway_keys_chat;
ALTER TABLE gateway_keys DROP COLUMN chat_id;
