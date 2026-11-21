-- Every model is named with its provider (issue #1002, owner 2026-11-20): a Task's stored bare model gets, once, the
-- provider the code before dispatched it under. The change needs the code's own resolution (the Task's runtime, its
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

-- +goose Down
-- The code before reads a Task's model as a bare name and places it at the one provider that offers it to the Task's
-- runtime (checkModel then). A Task the conversion changed gets back the model it had. Any other named with its
-- provider, as Tasks made after the upgrade are, gets its model's own name only where that rule picks the same
-- provider again: among the kinds of the Task's own runtime, else its profile's (main's runtimeProviders), and among
-- every provider for a role Task, whose runtime its role chain picks; where it would not, it keeps the provider, as
-- the code before refuses it either way (review of PR #1000, round 3). A Task left unresolved kept its bare name.
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
), named (id, provider, bare, runtime) AS (
    SELECT t.id, p.name, substr(t.model, length(p.name) + 2),
           coalesce(nullif(t.runtime, ''), (
               SELECT v.runtime FROM profiles pr JOIN profile_versions v ON v.id = pr.current_version_id
               WHERE pr.name = t.profile))
    FROM tasks t JOIN providers p ON substr(t.model, 1, length(p.name) + 1) = p.name || '/'
    WHERE t.model_unresolved = 0 AND t.id NOT IN (SELECT task_id FROM model_name_changes)
), fits (id, provider, bare, offers) AS (
    SELECT n.id, n.provider, n.bare, (
        SELECT group_concat(m.provider) FROM models m JOIN providers p ON p.name = m.provider
        WHERE m.name = n.bare
          AND (p.kind IN (SELECT kind FROM kinds WHERE kinds.runtime = n.runtime)
               OR NOT EXISTS (SELECT 1 FROM kinds WHERE kinds.runtime = n.runtime)))
    FROM named n
)
UPDATE tasks
SET model = (SELECT bare FROM fits WHERE fits.id = tasks.id)
WHERE id IN (SELECT id FROM fits WHERE offers = provider);

UPDATE tasks
SET model = (SELECT c.model FROM model_name_changes c WHERE c.task_id = tasks.id)
WHERE id IN (SELECT task_id FROM model_name_changes);

ALTER TABLE tasks DROP COLUMN model_unresolved;
DROP TABLE model_name_changes;
DROP TABLE model_name_conversion;
