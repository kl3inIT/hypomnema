-- A Routine's run is a snapshot of its Routine when it starts (issue #1023, reviews of #1037): its access, its
-- prompt, its script's name, text and limit. The launch, the wake and every check of the run's token read the run's
-- own copy, so a change of the Routine meanwhile, the owner's or a Bot's, changes nothing of the run that goes on.
-- +goose Up
ALTER TABLE routine_runs ADD COLUMN access TEXT NOT NULL DEFAULT 'full' CHECK (access IN ('full', 'read'));
ALTER TABLE routine_runs ADD COLUMN prompt TEXT NOT NULL DEFAULT '';
ALTER TABLE routine_runs ADD COLUMN script TEXT NOT NULL DEFAULT '';
ALTER TABLE routine_runs ADD COLUMN script_text TEXT NOT NULL DEFAULT '';
ALTER TABLE routine_runs ADD COLUMN timeout_seconds INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE routine_runs DROP COLUMN timeout_seconds;
ALTER TABLE routine_runs DROP COLUMN script_text;
ALTER TABLE routine_runs DROP COLUMN script;
ALTER TABLE routine_runs DROP COLUMN prompt;
ALTER TABLE routine_runs DROP COLUMN access;
