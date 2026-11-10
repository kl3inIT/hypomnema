-- +goose Up
-- Agents use the accounts the owner connected (issue #103, step 3).

-- The Connections a Run's workers may use, as "integration:label" joined by commas; NULL for none. A Run uses
-- only what it was given when it was created.
ALTER TABLE runs ADD COLUMN uses TEXT;

-- Whether the coordinator may use the Connection in chats: the owner's own switch, off until he turns it on.
ALTER TABLE connections ADD COLUMN chats INTEGER NOT NULL DEFAULT 0;

-- A Dispatch's own key to the agent API: made as its offer is sent, revoked when it settles, as its model
-- gateway key is (issue #107). Only the key's hash is kept. uses is what the key may ask a token for.
CREATE TABLE agent_sessions (
    id          TEXT PRIMARY KEY,
    key_hash    TEXT    NOT NULL UNIQUE,
    dispatch_id TEXT    NOT NULL,
    uses        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    revoked_at  INTEGER
) STRICT;

CREATE INDEX agent_sessions_dispatch ON agent_sessions (dispatch_id) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE agent_sessions;

ALTER TABLE connections DROP COLUMN chats;

ALTER TABLE runs DROP COLUMN uses;
