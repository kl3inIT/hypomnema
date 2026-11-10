-- The vault's secret store (issue #66), after Omnara's secrets and secret_versions
-- (migrations/000001_identity.sql). Only internal/controlplane/vault reads and writes these tables; they are in
-- this one migration stream so that `hypomnema migrate` and the schema check stay one thing.

-- +goose Up
CREATE TABLE secrets (
    id                 TEXT    NOT NULL PRIMARY KEY,
    name               TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    kind               TEXT    NOT NULL CHECK (kind IN ('generic', 'oauth_token_set')),
    -- Not secret: what the owner needs to recognize it (an email, a plan), as a JSON object of strings.
    metadata           TEXT    NOT NULL DEFAULT '{}',
    -- The version in use; a secret always has one.
    current_version_id TEXT    NOT NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
) STRICT;

-- One value of a secret, encrypted (aes-256-gcm-envelope-v1): the payload under a data key of its own, the data
-- key under the root key named by key_id. Both are bound to the secret, the version and the kind.
CREATE TABLE secret_versions (
    id                      TEXT    NOT NULL PRIMARY KEY,
    secret_id               TEXT    NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
    version_number          INTEGER NOT NULL CHECK (version_number > 0),
    -- The names of the payload's entries as a JSON array, never their values.
    payload_keys            TEXT    NOT NULL,
    encryption_scheme       TEXT    NOT NULL CHECK (encryption_scheme = 'aes-256-gcm-envelope-v1'),
    key_id                  TEXT    NOT NULL CHECK (length(key_id) > 0),
    encrypted_dek           BLOB    NOT NULL CHECK (length(encrypted_dek) = 48),
    encrypted_dek_nonce     BLOB    NOT NULL CHECK (length(encrypted_dek_nonce) = 12),
    nonce                   BLOB    NOT NULL CHECK (length(nonce) = 12),
    ciphertext              BLOB    NOT NULL CHECK (length(ciphertext) > 16),
    -- When an oauth_token_set's access token ends; NULL when unknown or for another kind.
    access_token_expires_at INTEGER,
    created_at              INTEGER NOT NULL,
    UNIQUE (secret_id, version_number)
) STRICT;

CREATE INDEX secret_versions_key ON secret_versions (key_id);

-- +goose Down
DROP INDEX secret_versions_key;
DROP TABLE secret_versions;
DROP TABLE secrets;
