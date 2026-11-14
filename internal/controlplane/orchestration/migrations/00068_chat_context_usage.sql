-- How full the chat's context window is (issue #384): pi's own report, asked with get_session_stats after each of
-- its turns and after a compaction, as Orca does (main/pi/rpc/rpc-turns.ts refreshUsage), and kept with the chat so
-- that the composer's meter shows it after pi was stopped and before it is started again. context_window is NULL
-- until pi first reported; context_tokens and context_percent are NULL right after a compaction, until pi's next
-- answer measures them again (pi docs/rpc-commands.md, get_session_stats).

-- +goose Up
ALTER TABLE chats ADD COLUMN context_tokens INTEGER;
ALTER TABLE chats ADD COLUMN context_window INTEGER;
ALTER TABLE chats ADD COLUMN context_percent REAL;

-- +goose Down
ALTER TABLE chats DROP COLUMN context_percent;
ALTER TABLE chats DROP COLUMN context_window;
ALTER TABLE chats DROP COLUMN context_tokens;
