-- The client API (issue #41): tokens for the owner and for coordinators, and the coordinator of each Run.

-- +goose Up
CREATE TABLE client_tokens (
    id         TEXT    NOT NULL PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    -- owner: everything; coordinator: its own Runs, and reading the rest.
    kind       TEXT    NOT NULL CHECK (kind IN ('owner', 'coordinator')),
    -- SHA-256 of the token, hex; the token itself is shown once and never stored.
    token_hash TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER
) STRICT;

-- The coordinator token that created the Run and coordinates it; NULL when the owner did.
ALTER TABLE runs ADD COLUMN coordinator_token_id TEXT REFERENCES client_tokens (id);

CREATE INDEX runs_created ON runs (created_at, id);

-- +goose Down
DROP INDEX runs_created;
ALTER TABLE runs DROP COLUMN coordinator_token_id;
DROP TABLE client_tokens;
