-- The event log (issue #19, PR 4). Every change writes its events in the same transaction, so the log
-- has no event without its change and no change without its event. One writer (BEGIN IMMEDIATE on one
-- connection) means a later commit always has higher seq values: a reader past seq n never misses an
-- event below n.

-- +goose Up
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL CHECK (kind IN (
                    'run.created', 'run.finished', 'run.canceled',
                    'task.created', 'task.status',
                    'dispatch.created', 'dispatch.status',
                    'conversation.closed',
                    'question.opened', 'question.answered', 'question.forwarded', 'question.canceled',
                    'escalation')),
    run_id      TEXT,
    task_id     TEXT,
    dispatch_id TEXT,
    question_id TEXT,
    node_id     TEXT,
    -- JSON object: ids, statuses and reasons; content is read from its own record.
    payload     TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX events_run ON events (run_id, seq) WHERE run_id IS NOT NULL;
CREATE INDEX events_task ON events (task_id, seq) WHERE task_id IS NOT NULL;

-- +goose Down
DROP TABLE events;
