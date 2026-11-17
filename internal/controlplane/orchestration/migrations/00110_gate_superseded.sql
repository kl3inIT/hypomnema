-- A follow-up to a Task blocked at a Gate replaces that Gate (issue #714, decision 9): the Gate is closed with the
-- reason superseded, and the Task goes on with the follow-up's note. Omnara cancels the open questions of a session
-- when new input arrives, with the reason superseded_by_input (commands_interactions.go:340-362).
--
-- SQLite cannot change a CHECK in place, so the questions table is made again with the new reason, as 00003 makes
-- dispatches again. No table references questions, so nothing else changes.

-- +goose Up
CREATE TABLE questions_new (
    id            TEXT    NOT NULL PRIMARY KEY,
    dispatch_id   TEXT    NOT NULL REFERENCES dispatches (id),
    task_id       TEXT    NOT NULL REFERENCES tasks (id),
    run_id        TEXT    NOT NULL REFERENCES runs (id),
    mode          TEXT    NOT NULL CHECK (mode IN ('wait', 'gate')),
    addressed_to  TEXT    NOT NULL CHECK (addressed_to IN ('coordinator', 'owner')),
    text          TEXT    NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
    -- JSON list of {label, allows_text}; empty means a free-text answer.
    options       TEXT    NOT NULL,
    status        TEXT    NOT NULL CHECK (status IN ('open', 'answered', 'canceled')),
    -- superseded: a follow-up replaced the open Gate (00110).
    cancel_reason TEXT    CHECK (cancel_reason IN ('dispatch_ended', 'task_canceled', 'superseded')),
    -- JSON {option, text}.
    answer        TEXT,
    answered_by   TEXT    CHECK (answered_by IN ('coordinator', 'owner')),
    answered_at   INTEGER,
    -- When the node acknowledged a wait answer.
    delivered_at  INTEGER,
    -- When the owner is next reminded of an open question.
    remind_at     INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK ((status = 'answered') = (answer IS NOT NULL AND answered_by IS NOT NULL AND answered_at IS NOT NULL)),
    CHECK ((status = 'canceled') = (cancel_reason IS NOT NULL))
) STRICT;

INSERT INTO questions_new SELECT * FROM questions;
DROP TABLE questions;
ALTER TABLE questions_new RENAME TO questions;

CREATE UNIQUE INDEX questions_one_open_wait_per_dispatch ON questions (dispatch_id)
    WHERE mode = 'wait' AND status = 'open';
CREATE UNIQUE INDEX questions_one_open_gate_per_task ON questions (task_id)
    WHERE mode = 'gate' AND status = 'open';
CREATE INDEX questions_run_status ON questions (run_id, status, created_at);
CREATE INDEX questions_addressee_status ON questions (addressed_to, status, created_at);
CREATE INDEX questions_dispatch ON questions (dispatch_id, created_at);
CREATE INDEX questions_task ON questions (task_id, created_at);
CREATE INDEX questions_remind ON questions (remind_at) WHERE status = 'open';

-- +goose Down
-- Back to 00003's reasons; fails if a Gate was superseded, which that table cannot hold.
CREATE TABLE questions_old (
    id            TEXT    NOT NULL PRIMARY KEY,
    dispatch_id   TEXT    NOT NULL REFERENCES dispatches (id),
    task_id       TEXT    NOT NULL REFERENCES tasks (id),
    run_id        TEXT    NOT NULL REFERENCES runs (id),
    mode          TEXT    NOT NULL CHECK (mode IN ('wait', 'gate')),
    addressed_to  TEXT    NOT NULL CHECK (addressed_to IN ('coordinator', 'owner')),
    text          TEXT    NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
    options       TEXT    NOT NULL,
    status        TEXT    NOT NULL CHECK (status IN ('open', 'answered', 'canceled')),
    cancel_reason TEXT    CHECK (cancel_reason IN ('dispatch_ended', 'task_canceled')),
    answer        TEXT,
    answered_by   TEXT    CHECK (answered_by IN ('coordinator', 'owner')),
    answered_at   INTEGER,
    delivered_at  INTEGER,
    remind_at     INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK ((status = 'answered') = (answer IS NOT NULL AND answered_by IS NOT NULL AND answered_at IS NOT NULL)),
    CHECK ((status = 'canceled') = (cancel_reason IS NOT NULL))
) STRICT;

INSERT INTO questions_old SELECT * FROM questions;
DROP TABLE questions;
ALTER TABLE questions_old RENAME TO questions;

CREATE UNIQUE INDEX questions_one_open_wait_per_dispatch ON questions (dispatch_id)
    WHERE mode = 'wait' AND status = 'open';
CREATE UNIQUE INDEX questions_one_open_gate_per_task ON questions (task_id)
    WHERE mode = 'gate' AND status = 'open';
CREATE INDEX questions_run_status ON questions (run_id, status, created_at);
CREATE INDEX questions_addressee_status ON questions (addressed_to, status, created_at);
CREATE INDEX questions_dispatch ON questions (dispatch_id, created_at);
CREATE INDEX questions_task ON questions (task_id, created_at);
CREATE INDEX questions_remind ON questions (remind_at) WHERE status = 'open';
