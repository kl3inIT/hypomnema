-- Agent profiles (issue #42), after Omnara's agent configs and profiles: a named profile points at its latest
-- immutable version; providers and the models they offer are their own records. Profiles replace the role defaults
-- that HYPOMNEMA_ROLES set: a Task names a profile instead of a role.

-- +goose Up
CREATE TABLE providers (
    name        TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    kind        TEXT    NOT NULL
        CHECK (kind IN ('anthropic', 'openai_compatible', 'claude_subscription', 'codex_subscription')),
    base_url    TEXT,
    -- Where the API key is kept, by name only (an environment variable or, later, a vault entry); never its value.
    api_key_ref TEXT,
    -- The provider's YAML as applied.
    source      TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE models (
    provider TEXT NOT NULL REFERENCES providers (name) ON DELETE CASCADE,
    name     TEXT NOT NULL CHECK (length(name) > 0),
    PRIMARY KEY (provider, name)
) STRICT;

CREATE TABLE profiles (
    name               TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    -- The latest version; a profile always has one.
    current_version_id TEXT    NOT NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
) STRICT;

CREATE TABLE profile_versions (
    id          TEXT    NOT NULL PRIMARY KEY,
    profile     TEXT    NOT NULL REFERENCES profiles (name),
    version     INTEGER NOT NULL CHECK (version > 0),
    -- The profile's YAML as applied, and its SHA-256 with line endings normalized.
    source      TEXT    NOT NULL,
    digest      TEXT    NOT NULL,
    runtime     TEXT    NOT NULL CHECK (length(runtime) > 0),
    provider    TEXT    REFERENCES providers (name),
    model       TEXT,
    effort      TEXT,
    -- Appended to the worker's built-in instructions.
    instruction TEXT,
    created_at  INTEGER NOT NULL,
    UNIQUE (profile, version)
) STRICT;

-- The starting set. Workers run Claude Code with its default model until a profile names one.
INSERT INTO providers (name, kind, source, created_at, updated_at)
VALUES ('claude-subscription', 'claude_subscription',
        'name: claude-subscription
kind: claude_subscription
models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, opus, sonnet, haiku]
', 0, 0);

INSERT INTO models (provider, name)
VALUES ('claude-subscription', 'claude-opus-5-5'), ('claude-subscription', 'claude-sonnet-5-5'),
       ('claude-subscription', 'claude-haiku-5-5'), ('claude-subscription', 'opus'),
       ('claude-subscription', 'sonnet'), ('claude-subscription', 'haiku');

INSERT INTO profiles (name, current_version_id, created_at, updated_at)
VALUES ('implementer', 'pfv_seed_implementer', 0, 0), ('reviewer', 'pfv_seed_reviewer', 0, 0),
       ('explorer', 'pfv_seed_explorer', 0, 0), ('coordinator', 'pfv_seed_coordinator', 0, 0);

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, instruction, created_at)
VALUES ('pfv_seed_implementer', 'implementer', 1, 'name: implementer
runtime: claude
', 'd143e4e0993dfd94c3f9f8557e7a5c3fdc34d088e43c3270a049f2ad768fe3b9', 'claude', NULL, 0),
       ('pfv_seed_reviewer', 'reviewer', 1, 'name: reviewer
runtime: claude
instruction: |
  Review the change the task names. Report what is wrong or missing, with file and line, as the summary of
  `finish`; change files only when the task asks for fixes.
', 'c68c06f12b2b5908e913314adcc175ac044a3c8aa3c6857af3755c4199fed447', 'claude', 'Review the change the task names. Report what is wrong or missing, with file and line, as the summary of
`finish`; change files only when the task asks for fixes.
', 0),
       ('pfv_seed_explorer', 'explorer', 1, 'name: explorer
runtime: claude
instruction: |
  Read and report only: do not change, commit or push files, and open no pull request. Put what you found, with
  file paths, in the summary of `finish`.
', 'f04739090a53b940b8f7e9063ac237afc118856119cec676613e9afeef5b6319', 'claude', 'Read and report only: do not change, commit or push files, and open no pull request. Put what you found, with
file paths, in the summary of `finish`.
', 0),
       ('pfv_seed_coordinator', 'coordinator', 1, 'name: coordinator
runtime: pi
', 'e1a48c0a709e6f74014b8fc21f711c86371ab579b0a4b62404868e72fda2e035', 'pi', NULL, 0);

-- Tasks name a profile; the only role used so far, implement, is the implementer profile.
ALTER TABLE tasks RENAME COLUMN role TO profile;
UPDATE tasks SET profile = 'implementer' WHERE profile = 'implement';

-- The profile version a Dispatch ran with; NULL for those created before profiles.
ALTER TABLE dispatches ADD COLUMN profile_version_id TEXT REFERENCES profile_versions (id);

-- +goose Down
ALTER TABLE dispatches DROP COLUMN profile_version_id;
UPDATE tasks SET profile = 'implement' WHERE profile = 'implementer';
ALTER TABLE tasks RENAME COLUMN profile TO role;
DROP TABLE profile_versions;
DROP TABLE profiles;
DROP TABLE models;
DROP TABLE providers;
