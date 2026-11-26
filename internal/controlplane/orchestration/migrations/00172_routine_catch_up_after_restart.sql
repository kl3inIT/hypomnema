-- +goose Up
-- A run a restart of the control plane cut gets one catch-up run (issue #1170): resume_run_id is that cut run while
-- the Routine waits for its catch-up, cleared when its next run starts, which keeps it as resumes_run_id and is told
-- what was cut. A catch-up that is cut itself gets none, so a restart loop does not repeat it (Hermes,
-- cron/missed_runs.py: missed slots collapse into one run; OpenClaw, src/cron/run-prompt.ts L86, the restart hint).
ALTER TABLE routines ADD COLUMN resume_run_id TEXT;
ALTER TABLE routine_runs ADD COLUMN resumes_run_id TEXT;

-- +goose Down
ALTER TABLE routine_runs DROP COLUMN resumes_run_id;
ALTER TABLE routines DROP COLUMN resume_run_id;
