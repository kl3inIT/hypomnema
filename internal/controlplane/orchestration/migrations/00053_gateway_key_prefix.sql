-- The start of a gateway key, so the owner tells keys apart on the Gateway keys page (issue #199, step 6), as
-- Coder shows a key's prefix (GatewayKeysPageView.tsx). The key itself is kept only as its hash; a key made before
-- this has none.

-- +goose Up
ALTER TABLE gateway_keys ADD COLUMN key_prefix TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gateway_keys DROP COLUMN key_prefix;
