-- +goose Up
-- A gateway key made for one Dispatch (issue #107): the worker's CLI calls the gateway with it, and it is revoked
-- when the Dispatch settles.
ALTER TABLE gateway_keys ADD COLUMN dispatch_id TEXT;
CREATE INDEX gateway_keys_dispatch ON gateway_keys (dispatch_id) WHERE dispatch_id IS NOT NULL;

-- +goose Down
DROP INDEX gateway_keys_dispatch;
ALTER TABLE gateway_keys DROP COLUMN dispatch_id;
