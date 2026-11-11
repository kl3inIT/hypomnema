-- Bots (issue #169): a Bot is a long-lived record of the control plane with its own model, persona and memory, apart
-- from the workers' Profiles (owner, 2026-11-11); after OpenMausBot's bot records and OpenClaw's agents.entries. pi
-- on the server ran as the Profile "coordinator" until now; every Profile of the runtime pi becomes a Bot, and a
-- chat records the Bot it talks with.

-- +goose Up
CREATE TABLE bots (
    name       TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    provider   TEXT    REFERENCES providers (name),
    model      TEXT,
    effort     TEXT,
    -- What the Bot is and how it behaves, written by the owner; given to pi after the built-in instructions.
    persona    TEXT    NOT NULL DEFAULT '',
    -- What the Bot keeps across chats, one entry per line; written through the control plane only.
    memory     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

INSERT INTO bots (name, provider, model, effort, persona, created_at, updated_at)
SELECT p.name, v.provider, v.model, v.effort, coalesce(v.instruction, ''), p.created_at, p.updated_at
FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id
WHERE v.runtime = 'pi';

-- The first Bot is always there: chats without another Bot talk with it.
INSERT INTO bots (name, created_at, updated_at) VALUES ('coordinator', 0, 0) ON CONFLICT DO NOTHING;

-- What was in pi's instruction file about giving work to workers is the first Bot's part, not every Bot's.
UPDATE bots SET persona = 'You are the owner''s coordinator in Hypomnema. The owner writes to you in a chat; you give coding work to
Hypomnema workers on the owner''s nodes and report what they did.

- Load the `hypomnema-coordinator` skill whenever the owner asks for work in a repository, and follow it: it names
  the `hypomnema` commands this machine accepts.
- You keep no checkout and change no code: changes are Tasks for workers, who open pull requests the owner reviews.
  Read and run read-only commands as much as planning needs.
' WHERE name = 'coordinator' AND persona = '';

-- No Dispatch ran under a Profile of pi, so its versions go with it.
DELETE FROM profile_versions WHERE profile IN (
    SELECT p.name FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id WHERE v.runtime = 'pi'
);
DELETE FROM profiles WHERE name NOT IN (SELECT profile FROM profile_versions);

-- No REFERENCES: SQLite adds a referencing column only with a NULL default. A Bot with chats is not removed.
ALTER TABLE chats ADD COLUMN bot TEXT NOT NULL DEFAULT 'coordinator';

-- +goose Down
ALTER TABLE chats DROP COLUMN bot;

INSERT INTO profiles (name, current_version_id, created_at, updated_at)
SELECT name, 'pfv_bot_' || name, created_at, updated_at FROM bots;

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction, description, created_at)
SELECT 'pfv_bot_' || name, name, 1, 'name: ' || name || '
description: pi on the server.
runtime: pi
', '', 'pi', provider, model, effort, nullif(persona, ''), 'pi on the server.', created_at
FROM bots;
DROP TABLE bots;
