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
-- backups as models. Only what the API accepts and the version's runtime can run is converted
-- (profilestore.CheckFallbacksTx): a backup's model whose provider is of a kind the version's runtime does not run
-- against (a Claude subscription's model for a Codex Profile), or that the catalog no longer offers, is dropped, as
-- is a backup that names no model of its own (it ran its runtime's default), a role Profile, a name no Profile has,
-- a model named twice and the version's own model; a version with no model of its own keeps none, since fallbacks
-- follow a primary. What each version named is kept in legacy_profile_fallbacks, which Down puts back.
CREATE TABLE legacy_profile_fallbacks (
    version_id TEXT NOT NULL PRIMARY KEY,
    fallbacks  TEXT NOT NULL
) STRICT;

INSERT INTO legacy_profile_fallbacks (version_id, fallbacks)
SELECT id, fallbacks FROM profile_versions WHERE fallbacks <> '';

-- The provider kinds each runtime runs against, profilestore's runtimeProviders on 2026-11-28.
CREATE TEMP TABLE runtime_kinds (runtime TEXT, kind TEXT);

INSERT INTO runtime_kinds (runtime, kind)
SELECT runtime.column1, gateway.column1
FROM (VALUES ('claude'), ('pi'), ('omp'), ('opencode')) AS runtime
CROSS JOIN (VALUES ('model_gateway'), ('claude_subscription'), ('codex_subscription'),
    ('antigravity_subscription'), ('opencode_go'), ('opencode_zen')) AS gateway;

INSERT INTO runtime_kinds (runtime, kind) VALUES
    ('pi', 'anthropic'), ('pi', 'openai_compatible'),
    ('codex', 'openai_compatible'), ('codex', 'model_gateway'), ('codex', 'codex_subscription');

CREATE TEMP TABLE converted_fallbacks (version_id TEXT, position INTEGER, ref TEXT);

INSERT INTO converted_fallbacks (version_id, position, ref)
SELECT legacy.version_id, entry.key, backup.provider || '/' || backup.model
FROM legacy_profile_fallbacks AS legacy
JOIN profile_versions AS own ON own.id = legacy.version_id
CROSS JOIN json_each('[' || replace(json_quote(legacy.fallbacks), ',', '","') || ']') AS entry
JOIN profiles AS named ON named.name = entry.value
JOIN profile_versions AS backup ON backup.id = named.current_version_id
WHERE own.runtime <> '@role' AND own.provider IS NOT NULL AND own.model IS NOT NULL
  AND backup.runtime <> '@role' AND backup.provider IS NOT NULL AND backup.model IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM models AS m
      JOIN providers AS p ON p.name = m.provider
      JOIN runtime_kinds AS k ON k.kind = p.kind AND k.runtime = own.runtime
      WHERE m.provider = backup.provider AND m.name = backup.model
  );

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
DROP TABLE runtime_kinds;

-- A Bot's settings are versioned as a Profile's are (issue #1336): each save keeps what it set, who saved it and
-- when, as Paperclip's agent_config_revisions (packages/db/src/schema/agent_config_revisions.ts). Its memory is not
-- part of a version. A Bot gets its first version at its first save after this, which keeps what it had before.
CREATE TABLE bot_versions (
    id            TEXT    NOT NULL PRIMARY KEY,
    bot_id        TEXT    NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    version       INTEGER NOT NULL CHECK (version > 0),
    -- The settings as the setup changes keep them (setupchanges.go, botSettings), as JSON, with the instructions
    -- under instructions, where the setup changes keep persona for their undo's hash (botversions.go).
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

-- A version applied since was written in this release's names: its YAML is written back in the names the release
-- before reads, so applying or restoring it parses again. A top-level instructions key is instruction again, and its
-- fallbacks, models no backup Profile is, leave its YAML as they leave its column below. A version from before was
-- never rewritten and is left as it is. A line is the YAML's top level when it starts with none of a space, a tab, a
-- dash or a hash: a block's lines and a list's items are indented or start with a dash. Blank lines stay.
CREATE TEMP TABLE source_lines AS
WITH RECURSIVE split (version_id, n, line, rest) AS (
    SELECT id, 0, '', source || char(10) FROM profile_versions
    WHERE id NOT IN (SELECT version_id FROM legacy_profile_fallbacks)
      AND (substr(source, 1, 13) = 'instructions:' OR instr(source, char(10) || 'instructions:') > 0
           OR fallbacks <> '')
    UNION ALL
    SELECT version_id, n + 1, substr(rest, 1, instr(rest, char(10)) - 1), substr(rest, instr(rest, char(10)) + 1)
    FROM split WHERE rest <> ''
)
SELECT version_id, n, line FROM split WHERE n > 0;

CREATE TEMP TABLE source_keys AS
SELECT l.version_id, l.n, l.line, (
    SELECT k.line FROM source_lines AS k
    WHERE k.version_id = l.version_id AND k.n <= l.n AND k.line <> ''
      AND substr(k.line, 1, 1) NOT IN (' ', char(9), '-', '#')
    ORDER BY k.n DESC LIMIT 1
) AS key
FROM source_lines AS l;

UPDATE profile_versions SET source = coalesce((
    SELECT group_concat(line, char(10)) FROM (
        SELECT CASE WHEN substr(s.line, 1, 13) = 'instructions:' THEN 'instruction:' || substr(s.line, 14)
                    ELSE s.line END AS line
        FROM source_keys AS s
        WHERE s.version_id = profile_versions.id
          AND NOT (profile_versions.fallbacks <> '' AND s.line <> ''
                   AND substr(coalesce(s.key, ''), 1, 10) = 'fallbacks:')
        ORDER BY s.n
    )
), '')
WHERE id IN (SELECT version_id FROM source_keys);

DROP TABLE source_keys;
DROP TABLE source_lines;

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
