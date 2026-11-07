-- Questions and Gates (issue #19, PR 3). Times are Unix milliseconds from the injected clock.

-- +goose Up

-- dispatches gains the final status gated and three columns. SQLite cannot change a CHECK in place,
-- so the table is rebuilt as tasks was in 00002: its children are set aside first, so foreign keys
-- stay on throughout.
CREATE TABLE dispatches_new (
    id                TEXT    NOT NULL PRIMARY KEY,
    task_id           TEXT    NOT NULL REFERENCES tasks (id),
    conversation_id   TEXT    NOT NULL REFERENCES conversations (id),
    node_id           TEXT    NOT NULL CHECK (length(node_id) > 0),
    status            TEXT    NOT NULL CHECK (status IN
                          ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown',
                           'succeeded', 'failed', 'canceled', 'expired', 'abandoned', 'gated')),
    epoch             INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    failure_class     TEXT    CHECK (failure_class IN
                          ('start_failed', 'needs_auth', 'rate_limited', 'worker_error', 'lost')),
    unknown_cause     TEXT    CHECK (unknown_cause IN ('lease_lost', 'exited_unreported', 'stop_timeout')),
    recovery          INTEGER NOT NULL DEFAULT 0 CHECK (recovery IN (0, 1)),
    process_ref       TEXT,
    result            TEXT,
    resets_at         INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    accepted_at       INTEGER,
    started_at        INTEGER,
    lease_expires_at  INTEGER,
    progress_at       INTEGER,
    stop_requested_at INTEGER,
    stop_acked_at     INTEGER,
    stalled_at        INTEGER,
    overrun_at        INTEGER,
    settled_at        INTEGER,
    -- Set while the worker waits for the answer to a question; the stall and duration timers pause.
    waiting_since     INTEGER,
    -- Time spent waiting for answers, left out of the duration limit.
    waited_ms         INTEGER NOT NULL DEFAULT 0 CHECK (waited_ms >= 0),
    -- The answered Gate this Dispatch resumes from; its question and answer go to the worker.
    gate_question_id  TEXT,
    CHECK ((status = 'failed') = (failure_class IS NOT NULL)),
    CHECK ((status IN ('unknown', 'stop_unknown')) = (unknown_cause IS NOT NULL))
) STRICT;

INSERT INTO dispatches_new (id, task_id, conversation_id, node_id, status, epoch, failure_class, unknown_cause,
                            recovery, process_ref, result, resets_at, created_at, updated_at, accepted_at,
                            started_at, lease_expires_at, progress_at, stop_requested_at, stop_acked_at,
                            stalled_at, overrun_at, settled_at)
SELECT id, task_id, conversation_id, node_id, status, epoch, failure_class, unknown_cause,
       recovery, process_ref, result, resets_at, created_at, updated_at, accepted_at,
       started_at, lease_expires_at, progress_at, stop_requested_at, stop_acked_at,
       stalled_at, overrun_at, settled_at
FROM dispatches;

CREATE TEMP TABLE dispatch_effects_copy AS SELECT * FROM dispatch_effects;
CREATE TEMP TABLE dispatch_resources_copy AS SELECT * FROM dispatch_resources;
DROP TABLE dispatch_effects;
DROP TABLE dispatch_resources;
DROP TABLE dispatches;
ALTER TABLE dispatches_new RENAME TO dispatches;

CREATE UNIQUE INDEX dispatches_one_active_per_task ON dispatches (task_id)
    WHERE status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');
CREATE UNIQUE INDEX dispatches_one_active_per_conversation ON dispatches (conversation_id)
    WHERE status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');
CREATE INDEX dispatches_status ON dispatches (status);
CREATE INDEX dispatches_node_created ON dispatches (node_id, created_at);
CREATE INDEX dispatches_conversation_created ON dispatches (conversation_id, created_at);
CREATE INDEX dispatches_task_created ON dispatches (task_id, created_at);

CREATE TABLE dispatch_effects (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    seq         INTEGER NOT NULL CHECK (seq > 0),
    kind        TEXT    NOT NULL CHECK (length(kind) > 0),
    ref         TEXT    NOT NULL,
    phase       TEXT    NOT NULL CHECK (phase IN ('intent', 'done')),
    payload     TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (dispatch_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE dispatch_resources (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    kind        TEXT    NOT NULL CHECK (kind IN ('worktree', 'vault_session', 'process')),
    ref         TEXT    NOT NULL CHECK (length(ref) > 0),
    custody     TEXT    NOT NULL CHECK (custody IN ('owned', 'external')),
    created_at  INTEGER NOT NULL,
    released_at INTEGER,
    PRIMARY KEY (dispatch_id, kind, ref)
) STRICT, WITHOUT ROWID;

INSERT INTO dispatch_effects SELECT * FROM dispatch_effects_copy;
INSERT INTO dispatch_resources SELECT * FROM dispatch_resources_copy;
DROP TABLE dispatch_effects_copy;
DROP TABLE dispatch_resources_copy;

-- A question a worker asks the coordinator or the owner. wait: the worker stays alive and waits.
-- gate: the Dispatch ended as gated and the Task waits for the answer.
CREATE TABLE questions (
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
    cancel_reason TEXT    CHECK (cancel_reason IN ('dispatch_ended', 'task_canceled')),
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

-- A worker waits on one question at a time; a Task has at most one open Gate.
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
DROP TABLE questions;

-- Back to the 00002 dispatches table; fails if a Dispatch is gated, which it cannot hold.
CREATE TABLE dispatches_old (
    id                TEXT    NOT NULL PRIMARY KEY,
    task_id           TEXT    NOT NULL REFERENCES tasks (id),
    conversation_id   TEXT    NOT NULL REFERENCES conversations (id),
    node_id           TEXT    NOT NULL CHECK (length(node_id) > 0),
    status            TEXT    NOT NULL CHECK (status IN
                          ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown',
                           'succeeded', 'failed', 'canceled', 'expired', 'abandoned')),
    epoch             INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    failure_class     TEXT    CHECK (failure_class IN
                          ('start_failed', 'needs_auth', 'rate_limited', 'worker_error', 'lost')),
    unknown_cause     TEXT    CHECK (unknown_cause IN ('lease_lost', 'exited_unreported', 'stop_timeout')),
    recovery          INTEGER NOT NULL DEFAULT 0 CHECK (recovery IN (0, 1)),
    process_ref       TEXT,
    result            TEXT,
    resets_at         INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    accepted_at       INTEGER,
    started_at        INTEGER,
    lease_expires_at  INTEGER,
    progress_at       INTEGER,
    stop_requested_at INTEGER,
    stop_acked_at     INTEGER,
    stalled_at        INTEGER,
    overrun_at        INTEGER,
    settled_at        INTEGER,
    CHECK ((status = 'failed') = (failure_class IS NOT NULL)),
    CHECK ((status IN ('unknown', 'stop_unknown')) = (unknown_cause IS NOT NULL))
) STRICT;

INSERT INTO dispatches_old (id, task_id, conversation_id, node_id, status, epoch, failure_class, unknown_cause,
                            recovery, process_ref, result, resets_at, created_at, updated_at, accepted_at,
                            started_at, lease_expires_at, progress_at, stop_requested_at, stop_acked_at,
                            stalled_at, overrun_at, settled_at)
SELECT id, task_id, conversation_id, node_id, status, epoch, failure_class, unknown_cause,
       recovery, process_ref, result, resets_at, created_at, updated_at, accepted_at,
       started_at, lease_expires_at, progress_at, stop_requested_at, stop_acked_at,
       stalled_at, overrun_at, settled_at
FROM dispatches;

CREATE TEMP TABLE dispatch_effects_copy AS SELECT * FROM dispatch_effects;
CREATE TEMP TABLE dispatch_resources_copy AS SELECT * FROM dispatch_resources;
DROP TABLE dispatch_effects;
DROP TABLE dispatch_resources;
DROP TABLE dispatches;
ALTER TABLE dispatches_old RENAME TO dispatches;

CREATE UNIQUE INDEX dispatches_one_active_per_task ON dispatches (task_id)
    WHERE status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');
CREATE UNIQUE INDEX dispatches_one_active_per_conversation ON dispatches (conversation_id)
    WHERE status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');
CREATE INDEX dispatches_status ON dispatches (status);
CREATE INDEX dispatches_node_created ON dispatches (node_id, created_at);
CREATE INDEX dispatches_conversation_created ON dispatches (conversation_id, created_at);

CREATE TABLE dispatch_effects (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    seq         INTEGER NOT NULL CHECK (seq > 0),
    kind        TEXT    NOT NULL CHECK (length(kind) > 0),
    ref         TEXT    NOT NULL,
    phase       TEXT    NOT NULL CHECK (phase IN ('intent', 'done')),
    payload     TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (dispatch_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE dispatch_resources (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    kind        TEXT    NOT NULL CHECK (kind IN ('worktree', 'vault_session', 'process')),
    ref         TEXT    NOT NULL CHECK (length(ref) > 0),
    custody     TEXT    NOT NULL CHECK (custody IN ('owned', 'external')),
    created_at  INTEGER NOT NULL,
    released_at INTEGER,
    PRIMARY KEY (dispatch_id, kind, ref)
) STRICT, WITHOUT ROWID;

INSERT INTO dispatch_effects SELECT * FROM dispatch_effects_copy;
INSERT INTO dispatch_resources SELECT * FROM dispatch_resources_copy;
DROP TABLE dispatch_effects_copy;
DROP TABLE dispatch_resources_copy;
