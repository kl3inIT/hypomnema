-- The Antigravity subscription as a built-in provider (issue #20, step 4), as codex-subscription is.
--
-- providers.kind was checked against a list in the table, which SQLite cannot change in place. The table is
-- rebuilt without the list: the kinds are checked where a provider is applied (orchestration.validProvider), so a
-- new kind needs no rebuild again. models rows go with their provider when it is dropped (ON DELETE CASCADE), so
-- they are kept aside and put back; the other tables that name a provider (profile_versions,
-- gateway_model_targets) keep their rows, whose check waits for the commit.

-- +goose Up
PRAGMA defer_foreign_keys = ON;

CREATE TABLE providers_kept AS SELECT * FROM providers;
CREATE TABLE models_kept AS SELECT provider, name FROM models;

DROP TABLE providers;

CREATE TABLE providers (
    name        TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    kind        TEXT    NOT NULL CHECK (length(kind) > 0),
    base_url    TEXT,
    -- Where the API key is kept, by name only (vault:<name>, an environment variable or a file); never its value.
    api_key_ref TEXT,
    -- The provider's YAML as applied.
    source      TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

-- Putting each provider back is what settles the rows of other tables that name it.
INSERT INTO providers (name, kind, base_url, api_key_ref, source, created_at, updated_at)
SELECT name, kind, base_url, api_key_ref, source, created_at, updated_at FROM providers_kept;

INSERT INTO models (provider, name) SELECT provider, name FROM models_kept;

DROP TABLE models_kept;
DROP TABLE providers_kept;

-- The chat models an Antigravity account is served, by the names the backend takes. The gateway serves only the
-- ones the owner puts in its model table.
INSERT INTO providers (name, kind, source, created_at, updated_at)
SELECT 'antigravity-subscription', 'antigravity_subscription',
       'name: antigravity-subscription
kind: antigravity_subscription
models: [gemini-4-argon-high, gemini-3.8-flash-high, gemini-3.1-pro-high, claude-sonnet-5-5-high, claude-opus-5-5-high, gpt-oss-120b-medium]
', 0, 0
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE name = 'antigravity-subscription');

INSERT OR IGNORE INTO models (provider, name)
VALUES ('antigravity-subscription', 'gemini-4-argon-high'), ('antigravity-subscription', 'gemini-3.8-flash-high'),
       ('antigravity-subscription', 'gemini-3.1-pro-high'), ('antigravity-subscription', 'claude-sonnet-5-5-high'),
       ('antigravity-subscription', 'claude-opus-5-5-high'), ('antigravity-subscription', 'gpt-oss-120b-medium');

-- +goose Down
DELETE FROM models WHERE provider = 'antigravity-subscription';
DELETE FROM providers WHERE name = 'antigravity-subscription';
