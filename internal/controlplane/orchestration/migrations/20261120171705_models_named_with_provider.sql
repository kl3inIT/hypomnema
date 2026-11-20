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
-- The code before reads a Task's model as a bare name. A Task the conversion changed gets back the model it had; any
-- other named with its provider, as Tasks made after the upgrade are, gets its model's own name, which that code
-- resolves by the provider offering it; a Task left unresolved kept its bare name.
UPDATE tasks
SET model = substr(model, instr(model, '/') + 1)
WHERE model IS NOT NULL AND model_unresolved = 0
  AND id NOT IN (SELECT task_id FROM model_name_changes)
  AND EXISTS (SELECT 1 FROM providers p WHERE substr(tasks.model, 1, length(p.name) + 1) = p.name || '/');

UPDATE tasks
SET model = (SELECT c.model FROM model_name_changes c WHERE c.task_id = tasks.id)
WHERE id IN (SELECT task_id FROM model_name_changes);

ALTER TABLE tasks DROP COLUMN model_unresolved;
DROP TABLE model_name_changes;
DROP TABLE model_name_conversion;
