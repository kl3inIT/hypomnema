-- +goose Up
-- A Bot's turn in progress is marked on its chat (issue #716), as Hermes writes a durable marker at a turn's start
-- and clears it when the turn concludes (tui_gateway/turn_marker.py): only the control plane's death leaves one, so
-- a mark found at start is a turn a restart cut. turn_attempts is how many times the control plane woke the chat to
-- continue that turn, Hermes's crash-loop breaker (session_auto_continue.py, max_attempts).
ALTER TABLE chats ADD COLUMN turn_started_at INTEGER;
ALTER TABLE chats ADD COLUMN turn_attempts INTEGER NOT NULL DEFAULT 0;

-- The starts that found a cut turn, Hermes's restart-loop guard (gateway/restart_loop_guard.py, restart_loop.json):
-- starts chained by short gaps mean restarts in a loop, and then no turn is continued.
CREATE TABLE restart_boots (
    at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE restart_boots;
ALTER TABLE chats DROP COLUMN turn_attempts;
ALTER TABLE chats DROP COLUMN turn_started_at;
