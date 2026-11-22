-- A Conversation is closed as `context_overflow` when its CLI session outgrew the model's context window, so the next
-- Dispatch starts a new session instead of resuming one that fails again (issue #1052).
--
-- SQLite cannot change a table's checks in place: conversations is rebuilt, as 00125 rebuilds meetings. Its rows are
-- kept aside and put back; dispatches, which references it, stays, and the foreign keys are checked at the commit,
-- once every Conversation is back.

-- +goose Up
PRAGMA defer_foreign_keys = ON;

CREATE TABLE conversations_kept AS SELECT * FROM conversations;

DROP INDEX conversations_node_worktree;
DROP INDEX conversations_one_open_per_task;
DROP TABLE conversations;

CREATE TABLE conversations (
    id              TEXT    NOT NULL PRIMARY KEY,
    task_id         TEXT    NOT NULL REFERENCES tasks (id),
    node_id         TEXT    NOT NULL CHECK (length(node_id) > 0),
    worktree        TEXT    NOT NULL CHECK (length(worktree) > 0),
    runtime         TEXT    NOT NULL CHECK (length(runtime) > 0),
    account_ref     TEXT    NOT NULL CHECK (length(account_ref) > 0),
    cli_session_ref TEXT,
    status          TEXT    NOT NULL CHECK (status IN ('open', 'closed')),
    close_reason    TEXT    CHECK (close_reason IN ('interrupted', 'never_started', 'superseded', 'context_overflow')),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    CHECK ((status = 'closed') = (close_reason IS NOT NULL))
) STRICT;

INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, cli_session_ref, status, close_reason,
                           created_at, updated_at)
SELECT id, task_id, node_id, worktree, runtime, account_ref, cli_session_ref, status, close_reason, created_at,
       updated_at
FROM conversations_kept;

DROP TABLE conversations_kept;

CREATE UNIQUE INDEX conversations_one_open_per_task ON conversations (task_id) WHERE status = 'open';
CREATE INDEX conversations_node_worktree ON conversations (node_id, worktree);

-- +goose Down
-- A Conversation closed for its overflow reads as superseded: a new one was opened for its Task.
PRAGMA defer_foreign_keys = ON;

CREATE TABLE conversations_kept AS SELECT * FROM conversations;

DROP INDEX conversations_node_worktree;
DROP INDEX conversations_one_open_per_task;
DROP TABLE conversations;

CREATE TABLE conversations (
    id              TEXT    NOT NULL PRIMARY KEY,
    task_id         TEXT    NOT NULL REFERENCES tasks (id),
    node_id         TEXT    NOT NULL CHECK (length(node_id) > 0),
    worktree        TEXT    NOT NULL CHECK (length(worktree) > 0),
    runtime         TEXT    NOT NULL CHECK (length(runtime) > 0),
    account_ref     TEXT    NOT NULL CHECK (length(account_ref) > 0),
    cli_session_ref TEXT,
    status          TEXT    NOT NULL CHECK (status IN ('open', 'closed')),
    close_reason    TEXT    CHECK (close_reason IN ('interrupted', 'never_started', 'superseded')),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    CHECK ((status = 'closed') = (close_reason IS NOT NULL))
) STRICT;

INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, cli_session_ref, status, close_reason,
                           created_at, updated_at)
SELECT id, task_id, node_id, worktree, runtime, account_ref, cli_session_ref, status,
       CASE close_reason WHEN 'context_overflow' THEN 'superseded' ELSE close_reason END, created_at, updated_at
FROM conversations_kept;

DROP TABLE conversations_kept;

CREATE UNIQUE INDEX conversations_one_open_per_task ON conversations (task_id) WHERE status = 'open';
CREATE INDEX conversations_node_worktree ON conversations (node_id, worktree);
