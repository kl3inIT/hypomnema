-- A Connection of an Integration of kind account (issue #203): the service is self-hosted, so its address belongs
-- to the Connection and not to the Integration, as the base URL of treg's endpoint tool does and as both Superset
-- MCP servers take it (Winding2020/superset-mcp, aptro/superset-mcp). The account's user name is the Connection's
-- account; its password is the vault secret the Connection names. version is what the service said it runs when
-- the Connection was made, when it could be read. sign_in_failed says the service refused the account the last
-- time the control plane signed in.

-- +goose Up
ALTER TABLE connections ADD COLUMN address TEXT;
ALTER TABLE connections ADD COLUMN version TEXT;
ALTER TABLE connections ADD COLUMN sign_in_failed INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE connections DROP COLUMN sign_in_failed;
ALTER TABLE connections DROP COLUMN version;
ALTER TABLE connections DROP COLUMN address;
