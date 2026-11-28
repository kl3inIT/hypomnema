-- Bots and Profiles share one definition of what they have in common (issue #1336).
-- +goose Up
-- One name for what an agent is told: a Bot's persona and a Profile's instruction are its instructions, as
-- Omnigent's agent spec and OpenClaw's agent entry call them. The client API reads the old names for one release.
ALTER TABLE bots RENAME COLUMN persona TO instructions;
ALTER TABLE profile_versions RENAME COLUMN instruction TO instructions;

-- A Profile's fallbacks were the names of backup Profiles a Task ran under while its own runtime's accounts rested
-- (issue #171). They are now what a Bot's are, an ordered provider/model list (OpenClaw's model fallbacks), run on
-- the Profile's own runtime. Note: each version that named backups, the current one and those before it, gets the
-- model each backup's current version runs, in the same order, so restoring an old version restores the same
-- backups as models. A backup that names no model of its own (it ran its runtime's default), a role Profile, a
-- name no Profile has, a model named twice and the version's own model are dropped: no model to convert them to.
-- What each version named is kept in legacy_profile_fallbacks, which Down puts back.
CREATE TABLE legacy_profile_fallbacks (
    version_id TEXT NOT NULL PRIMARY KEY,
    fallbacks  TEXT NOT NULL
) STRICT;

INSERT INTO legacy_profile_fallbacks (version_id, fallbacks)
SELECT id, fallbacks FROM profile_versions WHERE fallbacks <> '';

CREATE TEMP TABLE converted_fallbacks (version_id TEXT, position INTEGER, ref TEXT);

INSERT INTO converted_fallbacks (version_id, position, ref)
SELECT legacy.version_id, entry.key, backup.provider || '/' || backup.model
FROM legacy_profile_fallbacks AS legacy
CROSS JOIN json_each('[' || replace(json_quote(legacy.fallbacks), ',', '","') || ']') AS entry
JOIN profiles AS named ON named.name = entry.value
JOIN profile_versions AS backup ON backup.id = named.current_version_id
WHERE backup.runtime <> '@role' AND backup.provider IS NOT NULL AND backup.model IS NOT NULL;

DELETE FROM converted_fallbacks
WHERE EXISTS (
    SELECT 1 FROM converted_fallbacks AS earlier
    WHERE earlier.version_id = converted_fallbacks.version_id AND earlier.ref = converted_fallbacks.ref
      AND earlier.position < converted_fallbacks.position
) OR EXISTS (
    SELECT 1 FROM profile_versions AS own
    WHERE own.id = converted_fallbacks.version_id AND own.provider || '/' || own.model = converted_fallbacks.ref
);

UPDATE profile_versions SET fallbacks = coalesce((
    SELECT group_concat(ref) FROM (
        SELECT ref FROM converted_fallbacks WHERE converted_fallbacks.version_id = profile_versions.id
        ORDER BY position
    )
), '')
WHERE id IN (SELECT version_id FROM legacy_profile_fallbacks);

DROP TABLE converted_fallbacks;

-- A Bot's settings are versioned as a Profile's are (issue #1336): each save keeps what it set, who saved it and
-- when, as Paperclip's agent_config_revisions (packages/db/src/schema/agent_config_revisions.ts). Its memory is not
-- part of a version. A Bot gets its first version at its first save after this, which keeps what it had before.
CREATE TABLE bot_versions (
    id            TEXT    NOT NULL PRIMARY KEY,
    bot_id        TEXT    NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    version       INTEGER NOT NULL CHECK (version > 0),
    -- The settings as the setup changes keep them (setupchanges.go, botSettings), as JSON.
    settings      TEXT    NOT NULL,
    source        TEXT    NOT NULL CHECK (source IN ('save', 'restore', 'undo', 'before_versions')),
    -- owner or main_bot; NULL for before_versions.
    saved_by      TEXT    CHECK (saved_by IN ('owner', 'main_bot')),
    chat_id       TEXT,
    restored_from TEXT,
    created_at    INTEGER NOT NULL,
    UNIQUE (bot_id, version)
) STRICT;

-- +goose Down
DROP TABLE bot_versions;

UPDATE profile_versions SET fallbacks = (
    SELECT fallbacks FROM legacy_profile_fallbacks WHERE legacy_profile_fallbacks.version_id = profile_versions.id
)
WHERE id IN (SELECT version_id FROM legacy_profile_fallbacks);

-- A version applied since names models, which no backup Profile is: it names none.
UPDATE profile_versions SET fallbacks = ''
WHERE fallbacks <> '' AND id NOT IN (SELECT version_id FROM legacy_profile_fallbacks);

DROP TABLE legacy_profile_fallbacks;

ALTER TABLE profile_versions RENAME COLUMN instructions TO instruction;
ALTER TABLE bots RENAME COLUMN instructions TO persona;
