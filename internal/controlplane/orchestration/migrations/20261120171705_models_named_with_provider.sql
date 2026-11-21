-- Every model is named with its provider (issue #1002, owner 2026-11-20): a Task's stored bare model gets, once, the
-- provider the code before dispatched it under, and a profile version, a Bot or a chat that names a model without a
-- provider the one provider that offers it to its runtime (review of PR #1000, round 6). The change needs the code's own resolution (the Task's runtime, its
-- role chain, the kinds a runtime runs against), so Migrate makes it in Go right after this migration and before any
-- later one (orchestration/model_names.go nameStoredModels), and records here that it is due and what it changed.

-- +goose Up
CREATE TABLE model_name_conversion (
    -- 1 once the stored Task models are named with their provider.
    done INTEGER NOT NULL DEFAULT 0
);

INSERT INTO model_name_conversion (done) VALUES (0);

-- The model each changed Task had before, for the way back.
CREATE TABLE model_name_changes (
    task_id TEXT NOT NULL PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    model   TEXT NOT NULL
);

-- 1 for a Task whose model the code before would not have placed (two providers offered it to its runtime, or none):
-- its model is the bare name as it was, read whole, never at its slash, and refused when the Task is dispatched, as
-- the code before refused it (review of PR #1000).
ALTER TABLE tasks ADD COLUMN model_unresolved INTEGER NOT NULL DEFAULT 0;

-- Each profile version, Bot and chat given a provider, with the model and provider it was left with, for the way
-- back: record is profile_version, bot or chat.
CREATE TABLE model_provider_changes (
    record   TEXT NOT NULL CHECK (record IN ('profile_version', 'bot', 'chat')),
    id       TEXT NOT NULL,
    model    TEXT NOT NULL,
    provider TEXT NOT NULL,
    PRIMARY KEY (record, id)
);

-- +goose Down
-- The code before reads a Task's model as a bare name and places it at the one provider that offers it to the Task's
-- runtime, among that runtime's kinds then, which have no OpenCode Zen (checkModel and runtimeProviders before
-- issue #1002). A Task the conversion changed gets back the model it had. Any other named with its provider, as
-- Tasks made after the upgrade are, gets its model's own name only where that rule places it at that same provider,
-- and keeps the provider wherever it would not (reviews of PR #1000, rounds 3 and 4). The runtime is the Task's own,
-- else its profile's. A role Task's runtime is its role chain's first entry, written in role routing's YAML, which
-- SQL cannot read: its model loses its provider only when every runtime that takes the provider's kind would place
-- the bare name at that provider alone, the chain's runtime among them (it took the provider when the Task was
-- made), so a bare name never lands elsewhere or nowhere. A provider of a kind the code before has none of (Zen's,
-- under any name) is kept. A Task left unresolved kept its bare name.
WITH kinds (runtime, kind) AS (VALUES
    ('claude', 'model_gateway'), ('claude', 'claude_subscription'), ('claude', 'codex_subscription'),
    ('claude', 'antigravity_subscription'), ('claude', 'opencode_go'),
    ('codex', 'openai_compatible'), ('codex', 'model_gateway'), ('codex', 'codex_subscription'),
    ('pi', 'anthropic'), ('pi', 'openai_compatible'), ('pi', 'model_gateway'), ('pi', 'claude_subscription'),
    ('pi', 'codex_subscription'), ('pi', 'antigravity_subscription'), ('pi', 'opencode_go'),
    ('omp', 'model_gateway'), ('omp', 'claude_subscription'), ('omp', 'codex_subscription'),
    ('omp', 'antigravity_subscription'), ('omp', 'opencode_go'),
    ('opencode', 'model_gateway'), ('opencode', 'claude_subscription'), ('opencode', 'codex_subscription'),
    ('opencode', 'antigravity_subscription'), ('opencode', 'opencode_go')
), named (id, provider, kind, bare, runtime) AS (
    SELECT t.id, p.name, p.kind, substr(t.model, length(p.name) + 2),
           coalesce(nullif(t.runtime, ''), (
               SELECT v.runtime FROM profiles pr JOIN profile_versions v ON v.id = pr.current_version_id
               WHERE pr.name = t.profile))
    FROM tasks t JOIN providers p ON substr(t.model, 1, length(p.name) + 1) = p.name || '/'
    WHERE t.model_unresolved = 0 AND t.id NOT IN (SELECT task_id FROM model_name_changes)
), runtimes (id, runtime) AS (
    -- The Task's own or its profile's runtime, else every runtime that takes the provider's kind.
    SELECT n.id, n.runtime FROM named n WHERE n.runtime IN (SELECT runtime FROM kinds)
    UNION
    SELECT n.id, k.runtime FROM named n JOIN kinds k ON k.kind = n.kind
    WHERE n.runtime IS NULL OR n.runtime NOT IN (SELECT runtime FROM kinds)
), placed (id, offers) AS (
    -- The providers offering the bare name to each of those runtimes.
    SELECT r.id, (
        SELECT group_concat(m.provider) FROM models m JOIN providers p ON p.name = m.provider
        WHERE m.name = n.bare AND p.kind IN (SELECT kind FROM kinds WHERE kinds.runtime = r.runtime))
    FROM runtimes r JOIN named n ON n.id = r.id
)
UPDATE tasks
SET model = (SELECT bare FROM named WHERE named.id = tasks.id)
WHERE id IN (SELECT id FROM placed)
  AND NOT EXISTS (
    SELECT 1 FROM placed pl JOIN named n ON n.id = pl.id
    WHERE pl.id = tasks.id AND (pl.offers IS NULL OR pl.offers <> n.provider));

UPDATE tasks
SET model = (SELECT c.model FROM model_name_changes c WHERE c.task_id = tasks.id)
WHERE id IN (SELECT task_id FROM model_name_changes);

ALTER TABLE tasks DROP COLUMN model_unresolved;
-- A profile version, Bot or chat the conversion gave a provider is left without it again, as the code before ran it,
-- while it still names the model and the provider the conversion left it with; one changed since keeps what it
-- names, which the code before reads as it is.
UPDATE profile_versions SET provider = NULL
WHERE EXISTS (SELECT 1 FROM model_provider_changes c WHERE c.record = 'profile_version' AND c.id = profile_versions.id
              AND c.model = profile_versions.model AND c.provider = profile_versions.provider);

UPDATE bots SET provider = NULL
WHERE EXISTS (SELECT 1 FROM model_provider_changes c WHERE c.record = 'bot' AND c.id = bots.id
              AND c.model = bots.model AND c.provider = bots.provider);

UPDATE chats SET provider = NULL
WHERE EXISTS (SELECT 1 FROM model_provider_changes c WHERE c.record = 'chat' AND c.id = chats.id
              AND c.model = chats.model AND c.provider = chats.provider);

DROP TABLE model_provider_changes;
DROP TABLE model_name_changes;
DROP TABLE model_name_conversion;
