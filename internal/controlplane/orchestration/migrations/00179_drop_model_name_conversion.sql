-- The one-time naming of stored models with their provider (issue #1002) ran on every database (issue #1220,
-- 2026-11-27: no Bot, chat or profile version names a model without a provider, and the one Task left unresolved is
-- finished), so its marker, its change logs and the mark of a Task it could not place go.

-- +goose Up
DROP TABLE model_provider_changes;
DROP TABLE model_name_changes;
DROP TABLE model_name_conversion;
ALTER TABLE tasks DROP COLUMN model_unresolved;

-- +goose Down
-- The tables come back as the conversion left them: done, with no change to undo.
ALTER TABLE tasks ADD COLUMN model_unresolved INTEGER NOT NULL DEFAULT 0;

CREATE TABLE model_name_conversion (
    done INTEGER NOT NULL DEFAULT 0
);

INSERT INTO model_name_conversion (done) VALUES (1);

CREATE TABLE model_name_changes (
    task_id TEXT NOT NULL PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    model   TEXT NOT NULL
);

CREATE TABLE model_provider_changes (
    record   TEXT NOT NULL CHECK (record IN ('profile_version', 'bot', 'chat')),
    id       TEXT NOT NULL,
    model    TEXT NOT NULL,
    provider TEXT NOT NULL,
    PRIMARY KEY (record, id)
);
