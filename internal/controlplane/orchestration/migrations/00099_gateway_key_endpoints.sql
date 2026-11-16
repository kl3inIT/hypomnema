-- What a gateway key may use besides chat (issue #664): a JSON array of the kinds search, fetch and images, as
-- OmniRoute keeps allowed_endpoints on a key. NULL is every kind, as OmniRoute's empty list is: the keys made before,
-- and those of Dispatches and chats, keep every kind.

-- +goose Up
ALTER TABLE gateway_keys ADD COLUMN endpoints TEXT;

-- +goose Down
ALTER TABLE gateway_keys DROP COLUMN endpoints;
