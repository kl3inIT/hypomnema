-- Deleting a profile (issue #485). One that was never used goes with its versions; one that was used is marked
-- deleted and kept, with its versions, for the Dispatches that point at them, as Coder marks a template deleted
-- (coderd/templates.go, deleteTemplate) and Onyx a persona (backend/onyx/db/persona.py, mark_persona_as_deleted).
-- A deleted profile is hidden and no new Task names it; applying its name again clears the mark, as Onyx's
-- mark_persona_as_not_deleted.

-- +goose Up
ALTER TABLE profiles ADD COLUMN deleted_at INTEGER;

-- +goose Down
ALTER TABLE profiles DROP COLUMN deleted_at;
