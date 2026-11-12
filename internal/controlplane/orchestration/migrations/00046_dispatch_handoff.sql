-- A Dispatch that takes over a Task another worker began (issue #187): the Task's worker met a usage limit under
-- way, and a backup profile continues in the same worktree with a fresh session, as Orca hands a session to
-- another agent (src/renderer/src/lib/agent-session-continuation.ts) and OpenMausBot sends a limited turn's request
-- again on a backup (server/quota-switch.ts). Its worker is given the Task again, a note and the end of the
-- previous worker's transcript.

-- +goose Up
ALTER TABLE dispatches ADD COLUMN handoff INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE dispatches DROP COLUMN handoff;
