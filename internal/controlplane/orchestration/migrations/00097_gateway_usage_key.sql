-- The gateway's requests by key (issue #628): a revoked key is deleted once no request names it, and both that
-- check and the foreign key's on each delete look a key's requests up.

-- +goose Up
CREATE INDEX gateway_usage_key ON gateway_usage (key_id);

-- +goose Down
DROP INDEX gateway_usage_key;
