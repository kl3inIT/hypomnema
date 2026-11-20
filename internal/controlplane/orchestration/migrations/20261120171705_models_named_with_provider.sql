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

-- +goose Down
UPDATE tasks
SET model = (SELECT c.model FROM model_name_changes c WHERE c.task_id = tasks.id)
WHERE id IN (SELECT task_id FROM model_name_changes);

DROP TABLE model_name_changes;
DROP TABLE model_name_conversion;
