-- +goose Up
-- When the control plane began to stop with the chat's turn marked (issue #1162): written before it waits for the
-- turns, as Hermes's gateway writes resume_pending and last_resume_marked_at before its drain wait
-- (gateway/run_shutdown.py _mark_running_sessions_resume_pending). A cut turn is continued within an hour of it;
-- one without it (a crash) within an hour of turn_started_at. Cleared with the mark.
ALTER TABLE chats ADD COLUMN turn_cut_at INTEGER;

-- +goose Down
ALTER TABLE chats DROP COLUMN turn_cut_at;
