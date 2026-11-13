-- What a Bot may use (issue #169, part 3). The Connections it may use and the worker profiles it may give work to,
-- each a list after OpenMausBot's connector scopes (shared/connector-scopes.ts: absent means every app) and
-- OpenClaw's subagents.allowAgents: NULL is no list, every one; a list, even an empty one, is only those.
-- setup_changes are what the Bot coordinator made, changed or removed of Bots and profiles from a chat, each with
-- what undoes it, after OpenMausBot's team setup receipts (server/team-setup-requests.ts L393-425).

-- +goose Up
ALTER TABLE bots ADD COLUMN connections TEXT;
ALTER TABLE bots ADD COLUMN workers TEXT;

-- The Bot whose token created a Run, if any: a backup profile its Tasks fall back to must be on its workers list.
ALTER TABLE runs ADD COLUMN bot TEXT;

-- A Run a Bot created before this migration is the Bot's of the chat it was created in.
UPDATE runs SET bot = (SELECT chats.bot FROM chats WHERE chats.id = runs.chat_id)
WHERE chat_id IS NOT NULL AND coordinator_token_id IS NOT NULL;

CREATE TABLE setup_changes (
    id         TEXT    NOT NULL PRIMARY KEY,
    chat_id    TEXT    NOT NULL REFERENCES chats (id),
    kind       TEXT    NOT NULL CHECK (kind IN ('bot', 'profile')),
    name       TEXT    NOT NULL CHECK (length(name) > 0),
    action     TEXT    NOT NULL CHECK (action IN ('made', 'changed', 'removed')),
    -- What the thing was before, as JSON: a Bot's settings, or the id of a profile's version; NULL when made.
    before     TEXT,
    -- What it is after: a Bot's revision or a profile's version id; empty when removed. An undo of a thing whose
    -- revision is no longer this is refused.
    after      TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    undone_at  INTEGER
) STRICT;

CREATE INDEX setup_changes_chat ON setup_changes (chat_id, created_at);

-- +goose Down
DROP INDEX setup_changes_chat;
DROP TABLE setup_changes;
ALTER TABLE runs DROP COLUMN bot;
ALTER TABLE bots DROP COLUMN workers;
ALTER TABLE bots DROP COLUMN connections;
