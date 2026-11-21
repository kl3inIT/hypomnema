-- One state for every stored credential (issue #996, part A of the credential design #995): each records when it was
-- revoked and why, when it was last used (written at most once an hour), and when it expires where it has an end,
-- as Omnara's machine daemon tokens (revoked_at, revoke_reason, last_used_at touched hourly,
-- identitystore/auth_usage_touch.go) and Coder's api_keys (expires_at, last_used hourly, httpmw/apikey.go) do.
--
-- - A share link's token is no longer kept in plaintext: its SHA-256 finds it, and a copy sealed in the vault lets the
--   owner read the link again, as an owner-made gateway key's value is kept (migration 00131). SQL cannot hash or
--   seal, so the token of a link made before is left in `token` and the control plane moves it as it starts, before
--   anything is served (orchestration.Service.MoveShareTokens), then clears the files (ScrubShareTokens); a link
--   made after has none. Until then no link is served. Revoking a link now
--   marks it and drops its sealed copy instead of deleting the row; deleting its artifact still deletes it.
-- - A chat's gateway key names the coordinator token its pi runs under, so revoking that token ends the key.
-- - A web session is marked revoked when its browser signs out or its password changes, instead of being deleted.

-- +goose Up
CREATE TABLE artifact_shares_new (
    id                TEXT    NOT NULL PRIMARY KEY,
    artifact_id       TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    -- SHA-256 of the link's token, hex; NULL only for a link made before, until the control plane moves it.
    token_hash        TEXT UNIQUE CHECK (token_hash IS NULL OR length(token_hash) = 64),
    -- The plaintext token of a link made before this migration, cleared once it is moved.
    token             TEXT UNIQUE CHECK (token IS NULL OR length(token) = 43),
    -- The token sealed in the vault, which the owner reads the link from; NULL without a vault or once revoked.
    secret_version_id TEXT REFERENCES secret_versions (id),
    label             TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    created_at        INTEGER NOT NULL,
    views             INTEGER NOT NULL DEFAULT 0,
    -- The link's last use: written with its view count on each opening of its page (issue #745).
    last_viewed_at    INTEGER,
    revoked_at        INTEGER,
    revoke_reason     TEXT,
    CHECK (token_hash IS NOT NULL OR token IS NOT NULL)
) STRICT;

INSERT INTO artifact_shares_new (id, artifact_id, token, label, created_at, views, last_viewed_at)
SELECT id, artifact_id, token, label, created_at, views, last_viewed_at FROM artifact_shares;

DROP INDEX artifact_shares_artifact;
DROP TABLE artifact_shares;
ALTER TABLE artifact_shares_new RENAME TO artifact_shares;
CREATE INDEX artifact_shares_artifact ON artifact_shares (artifact_id, created_at);

-- That the database's files still hold old tokens in plaintext, in the rows not moved yet and in the pages the
-- rebuild above freed: kept until the control plane has moved every token and rebuilt and checkpointed the file
-- (orchestration.Service.ScrubShareTokens), so a stop in between is finished at the next start. No link is served
-- while it is here.
CREATE TABLE share_token_scrub (
    id    INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    since INTEGER NOT NULL
) STRICT;

INSERT INTO share_token_scrub (id, since)
SELECT 1, 0 WHERE EXISTS (SELECT 1 FROM artifact_shares WHERE token IS NOT NULL);

ALTER TABLE client_tokens ADD COLUMN revoke_reason TEXT;
ALTER TABLE client_tokens ADD COLUMN last_used_at INTEGER;

ALTER TABLE agent_sessions ADD COLUMN revoke_reason TEXT;
ALTER TABLE agent_sessions ADD COLUMN last_used_at INTEGER;

ALTER TABLE gateway_keys ADD COLUMN revoke_reason TEXT;
ALTER TABLE gateway_keys ADD COLUMN last_used_at INTEGER;
ALTER TABLE gateway_keys ADD COLUMN coordinator_token_id TEXT REFERENCES client_tokens (id);

ALTER TABLE node_tokens ADD COLUMN revoke_reason TEXT;

ALTER TABLE web_sessions ADD COLUMN revoked_at INTEGER;
ALTER TABLE web_sessions ADD COLUMN revoke_reason TEXT;
ALTER TABLE web_sessions ADD COLUMN last_used_at INTEGER;

-- A coordinator token revoked before this migration left its children working: the agent sessions and gateway keys
-- of the Dispatches of the Runs it created, finished or still running. They end now, with the reason a revocation
-- gives them from here on (orchestration.Service.RevokeToken).
UPDATE agent_sessions
SET revoked_at = CAST(unixepoch('subsec') * 1000 AS INTEGER), revoke_reason = 'parent_revoked'
WHERE revoked_at IS NULL AND dispatch_id IN (
    SELECT d.id FROM dispatches d JOIN tasks t ON t.id = d.task_id JOIN runs r ON r.id = t.run_id
    JOIN client_tokens c ON c.id = r.coordinator_token_id
    WHERE c.revoked_at IS NOT NULL);

UPDATE gateway_keys
SET revoked_at = CAST(unixepoch('subsec') * 1000 AS INTEGER), revoke_reason = 'parent_revoked'
WHERE revoked_at IS NULL AND dispatch_id IN (
    SELECT d.id FROM dispatches d JOIN tasks t ON t.id = d.task_id JOIN runs r ON r.id = t.run_id
    JOIN client_tokens c ON c.id = r.coordinator_token_id
    WHERE c.revoked_at IS NOT NULL);

-- +goose Down
DROP TABLE share_token_scrub;

DELETE FROM web_sessions WHERE revoked_at IS NOT NULL;
ALTER TABLE web_sessions DROP COLUMN last_used_at;
ALTER TABLE web_sessions DROP COLUMN revoke_reason;
ALTER TABLE web_sessions DROP COLUMN revoked_at;

ALTER TABLE node_tokens DROP COLUMN revoke_reason;

ALTER TABLE gateway_keys DROP COLUMN coordinator_token_id;
ALTER TABLE gateway_keys DROP COLUMN last_used_at;
ALTER TABLE gateway_keys DROP COLUMN revoke_reason;

ALTER TABLE agent_sessions DROP COLUMN last_used_at;
ALTER TABLE agent_sessions DROP COLUMN revoke_reason;

ALTER TABLE client_tokens DROP COLUMN last_used_at;
ALTER TABLE client_tokens DROP COLUMN revoke_reason;

-- Only a link whose plaintext token is still here can go back; one kept only hashed and sealed, and a revoked one,
-- are lost, with their sealed copies.
CREATE TEMP TABLE artifact_share_secrets AS
SELECT v.secret_id FROM artifact_shares s JOIN secret_versions v ON s.secret_version_id = v.id;

CREATE TABLE artifact_shares_old (
    id             TEXT    NOT NULL PRIMARY KEY,
    artifact_id    TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    token          TEXT    NOT NULL UNIQUE CHECK (length(token) = 43),
    label          TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    created_at     INTEGER NOT NULL,
    views          INTEGER NOT NULL DEFAULT 0,
    last_viewed_at INTEGER
) STRICT;

INSERT INTO artifact_shares_old (id, artifact_id, token, label, created_at, views, last_viewed_at)
SELECT id, artifact_id, token, label, created_at, views, last_viewed_at FROM artifact_shares
WHERE token IS NOT NULL AND revoked_at IS NULL;

DROP INDEX artifact_shares_artifact;
DROP TABLE artifact_shares;
ALTER TABLE artifact_shares_old RENAME TO artifact_shares;
CREATE INDEX artifact_shares_artifact ON artifact_shares (artifact_id, created_at);

DELETE FROM secrets WHERE id IN (SELECT secret_id FROM artifact_share_secrets);
DROP TABLE artifact_share_secrets;
