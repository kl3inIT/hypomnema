-- What a Bot may reach of a Connection it may use (issue #562): for a Superset Connection, the dashboards it may
-- read, by number, as JSON keyed by the Connection, {"superset:prod": {"dashboards": [9]}}. NULL, or a Connection
-- not named, is every dashboard the account sees, as before; after OpenMausBot's per-bot connector scopes
-- (packages/phone-protocol/src/connector-scopes.ts: absent means every app).

-- +goose Up
ALTER TABLE bots ADD COLUMN connection_scopes TEXT;

-- +goose Down
ALTER TABLE bots DROP COLUMN connection_scopes;
