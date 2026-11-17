-- +goose Up
-- A Bot's turn in progress is marked on its chat (issue #716), as Hermes writes a durable marker at a turn's start
-- and clears it when the turn concludes (tui_gateway/turn_marker.py): only the control plane's death leaves one, so
-- a mark found at start is a turn a restart cut. turn_token names the pi process whose turn it is, so that only
-- that process clears it (Hermes's compare-and-swap clear, gateway/session_lifecycle.py clear_active_turn_marker).
-- turn_attempts is how many times the control plane woke the chat to continue that turn, Hermes's crash-loop
-- breaker (session_auto_continue.py, max_attempts).
ALTER TABLE chats ADD COLUMN turn_started_at INTEGER;
ALTER TABLE chats ADD COLUMN turn_token TEXT;
ALTER TABLE chats ADD COLUMN turn_attempts INTEGER NOT NULL DEFAULT 0;

-- The starts that found a cut turn, Hermes's restart-loop guard (gateway/restart_loop_guard.py, restart_loop.json):
-- starts chained by short gaps mean restarts in a loop, and then no turn is continued.
CREATE TABLE restart_boots (
    at INTEGER NOT NULL
) STRICT;

-- A chat message's kind takes 'continued' (the control plane woke the Bot to continue a turn a restart cut; given
-- to pi) and 'cut' (a restart cut the turn and it is not continued). SQLite changes a CHECK only by rebuilding; the
-- column alone is made again, as 00071 did.
ALTER TABLE chat_messages ADD COLUMN kind_new TEXT NOT NULL DEFAULT ''
    CHECK (kind_new IN ('', 'wake', 'failed', 'stopped', 'approval', 'artifact', 'continued', 'cut'));
UPDATE chat_messages SET kind_new = kind;
ALTER TABLE chat_messages DROP COLUMN kind;
ALTER TABLE chat_messages RENAME COLUMN kind_new TO kind;

-- +goose Down
DELETE FROM chat_messages WHERE kind IN ('continued', 'cut');
ALTER TABLE chat_messages ADD COLUMN kind_old TEXT NOT NULL DEFAULT ''
    CHECK (kind_old IN ('', 'wake', 'failed', 'stopped', 'approval', 'artifact'));
UPDATE chat_messages SET kind_old = kind;
ALTER TABLE chat_messages DROP COLUMN kind;
ALTER TABLE chat_messages RENAME COLUMN kind_old TO kind;

DROP TABLE restart_boots;
ALTER TABLE chats DROP COLUMN turn_attempts;
ALTER TABLE chats DROP COLUMN turn_token;
ALTER TABLE chats DROP COLUMN turn_started_at;
