-- Every share link's token is kept hashed and sealed since issue #996, and the move of the links made before has run
-- (issue #1220, 2026-11-27: none of the five links keeps a plaintext token, and no scrub is owed). The plaintext column
-- and the mark of the owed scrub go; a link always has its hash. A UNIQUE column cannot be dropped in SQLite, so the
-- table is rebuilt, as migration 00151 rebuilt it.

-- +goose Up
CREATE TABLE artifact_shares_new (
    id                TEXT    NOT NULL PRIMARY KEY,
    artifact_id       TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    -- SHA-256 of the link's token, hex.
    token_hash        TEXT    NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    -- The token sealed in the vault, which the owner reads the link from; NULL without a vault or once revoked.
    secret_version_id TEXT REFERENCES secret_versions (id),
    label             TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    created_at        INTEGER NOT NULL,
    views             INTEGER NOT NULL DEFAULT 0,
    -- The link's last use: written with its view count on each opening of its page (issue #745).
    last_viewed_at    INTEGER,
    revoked_at        INTEGER,
    revoke_reason     TEXT
) STRICT;

INSERT INTO artifact_shares_new (id, artifact_id, token_hash, secret_version_id, label, created_at, views,
                                 last_viewed_at, revoked_at, revoke_reason)
SELECT id, artifact_id, token_hash, secret_version_id, label, created_at, views, last_viewed_at, revoked_at,
       revoke_reason
FROM artifact_shares;

DROP INDEX artifact_shares_artifact;
DROP TABLE artifact_shares;
ALTER TABLE artifact_shares_new RENAME TO artifact_shares;
CREATE INDEX artifact_shares_artifact ON artifact_shares (artifact_id, created_at);

DROP TABLE share_token_scrub;

-- +goose Down
-- The table as migration 00151 left it; every link comes back with its hash and no plaintext, and no scrub is owed.
CREATE TABLE share_token_scrub (
    id    INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    since INTEGER NOT NULL
) STRICT;

CREATE TABLE artifact_shares_old (
    id                TEXT    NOT NULL PRIMARY KEY,
    artifact_id       TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    token_hash        TEXT UNIQUE CHECK (token_hash IS NULL OR length(token_hash) = 64),
    token             TEXT UNIQUE CHECK (token IS NULL OR length(token) = 43),
    secret_version_id TEXT REFERENCES secret_versions (id),
    label             TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    created_at        INTEGER NOT NULL,
    views             INTEGER NOT NULL DEFAULT 0,
    last_viewed_at    INTEGER,
    revoked_at        INTEGER,
    revoke_reason     TEXT,
    CHECK (token_hash IS NOT NULL OR token IS NOT NULL)
) STRICT;

INSERT INTO artifact_shares_old (id, artifact_id, token_hash, secret_version_id, label, created_at, views,
                                 last_viewed_at, revoked_at, revoke_reason)
SELECT id, artifact_id, token_hash, secret_version_id, label, created_at, views, last_viewed_at, revoked_at,
       revoke_reason
FROM artifact_shares;

DROP INDEX artifact_shares_artifact;
DROP TABLE artifact_shares;
ALTER TABLE artifact_shares_old RENAME TO artifact_shares;
CREATE INDEX artifact_shares_artifact ON artifact_shares (artifact_id, created_at);
