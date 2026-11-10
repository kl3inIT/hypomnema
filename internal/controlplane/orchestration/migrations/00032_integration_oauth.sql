-- +goose Up
-- Integrations that sign in with OAuth (issue #103, step 2). A Connection of such an Integration is granted some of
-- its services, each to read or to read and write: "gmail:read,calendar". NULL for a git host's Connection.
ALTER TABLE connections ADD COLUMN services TEXT;

-- The OAuth client an Integration signs in with, one each. The client's id is not secret; its secret is a vault
-- secret, named here and never held here.
CREATE TABLE integration_clients (
    integration TEXT PRIMARY KEY,
    client_id   TEXT    NOT NULL,
    secret      TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE integration_clients;

ALTER TABLE connections DROP COLUMN services;
