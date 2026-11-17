-- A view-only link to an artifact (issue #745): anyone with it sees the artifact's newest revision without signing
-- in, until the owner revokes it. A link is a row, as open-webui's shared_chat, LibreChat's SharedLink and Grafana's
-- public dashboard are; several to one artifact, each named for whom it was sent (Grafana's per-viewer revoke).
-- The token is 32 random bytes in base64url, kept as it is so the owner can copy the link again (open-webui,
-- LibreChat and Grafana keep theirs so): it shows one artifact, whose bytes are in this database unencrypted.
-- Revoking deletes the row; deleting the artifact, with its chat, deletes its links.

-- +goose Up
CREATE TABLE artifact_shares (
    id             TEXT    NOT NULL PRIMARY KEY,
    artifact_id    TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    token          TEXT    NOT NULL UNIQUE CHECK (length(token) = 43),
    label          TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    created_at     INTEGER NOT NULL,
    -- How many times its page was opened (Grafana's request count, Metabase's anonymous view event).
    views          INTEGER NOT NULL DEFAULT 0,
    last_viewed_at INTEGER
) STRICT;

CREATE INDEX artifact_shares_artifact ON artifact_shares (artifact_id, created_at);

-- +goose Down
DROP INDEX artifact_shares_artifact;
DROP TABLE artifact_shares;
