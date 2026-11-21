-- The access of a Routine's run, taken from its Routine when the run starts (issue #1023, review of #1037): a read
-- run's check and chat keep reading only for the whole run, whatever the Routine is changed to meanwhile.
-- +goose Up
ALTER TABLE routine_runs ADD COLUMN access TEXT NOT NULL DEFAULT 'full' CHECK (access IN ('full', 'read'));

-- +goose Down
ALTER TABLE routine_runs DROP COLUMN access;
