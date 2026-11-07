-- Dispatches, Conversations, receipts, effects, resources and slot cooldowns (issue #19, PR 2).
-- Times are Unix milliseconds from the injected clock. Statuses and allowed transitions are listed
-- in transitions.go; the CHECKs here only keep the columns within those values.

-- +goose Up

-- tasks gains the blocked reason needs_auth and two columns. SQLite cannot change a CHECK in place,
-- so the table is rebuilt (sqlite.org/lang_altertable.html, "Making Other Kinds Of Table Schema
-- Changes"). task_deps is set aside first so that no foreign key ever points at a missing row and
-- the rebuild runs with foreign keys on, inside the migration's transaction.
CREATE TABLE tasks_new (
    id                  TEXT    NOT NULL PRIMARY KEY,
    run_id              TEXT    NOT NULL REFERENCES runs (id),
    position            INTEGER NOT NULL,
    role                TEXT    NOT NULL CHECK (length(role) > 0),
    spec                TEXT    NOT NULL CHECK (length(spec) > 0),
    runtime             TEXT,
    model               TEXT,
    effort              TEXT,
    status              TEXT    NOT NULL CHECK (status IN
                            ('pending', 'ready', 'running', 'blocked', 'succeeded', 'failed', 'canceled')),
    blocked_reason      TEXT    CHECK (blocked_reason IN
                            ('dependency_failed', 'gate', 'dispatch_unknown', 'needs_auth')),
    failure_count       INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    result              TEXT,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    finished_at         INTEGER,
    -- No Dispatch is created before this time (back-off after a failure, or a rate limit's reset).
    not_before          INTEGER,
    -- Set by a cancel while a Dispatch may still be running; the Task is canceled once it settles.
    cancel_requested_at INTEGER,
    CHECK ((status = 'blocked') = (blocked_reason IS NOT NULL)),
    UNIQUE (run_id, position)
) STRICT;

INSERT INTO tasks_new (id, run_id, position, role, spec, runtime, model, effort, status, blocked_reason,
                       failure_count, result, created_at, updated_at, finished_at)
SELECT id, run_id, position, role, spec, runtime, model, effort, status, blocked_reason,
       failure_count, result, created_at, updated_at, finished_at
FROM tasks;

CREATE TEMP TABLE task_deps_copy AS SELECT task_id, depends_on FROM task_deps;
DROP TABLE task_deps;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE INDEX tasks_run_status ON tasks (run_id, status);

CREATE TABLE task_deps (
    task_id    TEXT NOT NULL REFERENCES tasks (id),
    depends_on TEXT NOT NULL REFERENCES tasks (id),
    PRIMARY KEY (task_id, depends_on),
    CHECK (task_id <> depends_on)
) STRICT, WITHOUT ROWID;

INSERT INTO task_deps (task_id, depends_on) SELECT task_id, depends_on FROM task_deps_copy;
DROP TABLE task_deps_copy;
CREATE INDEX task_deps_depends_on ON task_deps (depends_on);

-- A Conversation is the worker's CLI session for one Task on one node, worktree, runtime and account.
-- node_id is plain text until the nodes table exists.
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

-- At most one open Conversation per Task.
CREATE UNIQUE INDEX conversations_one_open_per_task ON conversations (task_id) WHERE status = 'open';

-- A Dispatch is one attempt at a Task. epoch grows each time a node takes the Dispatch (accept,
-- reattach); a node call that names another epoch is refused.
CREATE TABLE dispatches (
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
    -- 1 when this Dispatch opened a new Conversation after an interrupted one: the worker is told to
    -- check the outcome of any interrupted operation before repeating it.
    recovery          INTEGER NOT NULL DEFAULT 0 CHECK (recovery IN (0, 1)),
    -- The worker process's identity (pid and start time), so a reattach proves it is the same process.
    process_ref       TEXT,
    -- The structured result, JSON (summary, evidence, exit, usage).
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

-- At most one Dispatch that may still be running, per Task and per Conversation.
CREATE UNIQUE INDEX dispatches_one_active_per_task ON dispatches (task_id)
    WHERE status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');
CREATE UNIQUE INDEX dispatches_one_active_per_conversation ON dispatches (conversation_id)
    WHERE status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');
CREATE INDEX dispatches_status ON dispatches (status);
CREATE INDEX dispatches_node_created ON dispatches (node_id, created_at);
CREATE INDEX dispatches_conversation_created ON dispatches (conversation_id, created_at);

-- One row per node call, so a repeated call returns the stored response (Orca's receipts).
CREATE TABLE receipts (
    caller       TEXT    NOT NULL,
    request_id   TEXT    NOT NULL,
    method       TEXT    NOT NULL,
    payload_hash TEXT    NOT NULL,
    response     TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (caller, request_id)
) STRICT;

CREATE INDEX receipts_created ON receipts (created_at);

-- Outside effects of a Dispatch (push, pull request, message): intent before, done after.
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

-- What a Dispatch holds on its node (worktree, vault session, process).
CREATE TABLE dispatch_resources (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    kind        TEXT    NOT NULL CHECK (kind IN ('worktree', 'vault_session', 'process')),
    ref         TEXT    NOT NULL CHECK (length(ref) > 0),
    custody     TEXT    NOT NULL CHECK (custody IN ('owned', 'external')),
    created_at  INTEGER NOT NULL,
    released_at INTEGER,
    PRIMARY KEY (dispatch_id, kind, ref)
) STRICT, WITHOUT ROWID;

-- A slot (node, runtime, account) that hit a rate limit takes no Dispatch until `until`.
CREATE TABLE slot_cooldowns (
    node_id     TEXT    NOT NULL,
    runtime     TEXT    NOT NULL,
    account_ref TEXT    NOT NULL,
    until       INTEGER NOT NULL,
    reason      TEXT    NOT NULL,
    PRIMARY KEY (node_id, runtime, account_ref)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE slot_cooldowns;
DROP TABLE dispatch_resources;
DROP TABLE dispatch_effects;
DROP TABLE receipts;
DROP TABLE dispatches;
DROP TABLE conversations;

-- Back to the PR 1 tasks table; fails if a Task is blocked for needs_auth, which it cannot hold.
CREATE TABLE tasks_old (
    id             TEXT    NOT NULL PRIMARY KEY,
    run_id         TEXT    NOT NULL REFERENCES runs (id),
    position       INTEGER NOT NULL,
    role           TEXT    NOT NULL CHECK (length(role) > 0),
    spec           TEXT    NOT NULL CHECK (length(spec) > 0),
    runtime        TEXT,
    model          TEXT,
    effort         TEXT,
    status         TEXT    NOT NULL CHECK (status IN
                       ('pending', 'ready', 'running', 'blocked', 'succeeded', 'failed', 'canceled')),
    blocked_reason TEXT    CHECK (blocked_reason IN ('dependency_failed', 'gate', 'dispatch_unknown')),
    failure_count  INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    result         TEXT,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    finished_at    INTEGER,
    CHECK ((status = 'blocked') = (blocked_reason IS NOT NULL)),
    UNIQUE (run_id, position)
) STRICT;

INSERT INTO tasks_old (id, run_id, position, role, spec, runtime, model, effort, status, blocked_reason,
                       failure_count, result, created_at, updated_at, finished_at)
SELECT id, run_id, position, role, spec, runtime, model, effort, status, blocked_reason,
       failure_count, result, created_at, updated_at, finished_at
FROM tasks;

CREATE TEMP TABLE task_deps_copy AS SELECT task_id, depends_on FROM task_deps;
DROP TABLE task_deps;
DROP TABLE tasks;
ALTER TABLE tasks_old RENAME TO tasks;
CREATE INDEX tasks_run_status ON tasks (run_id, status);

CREATE TABLE task_deps (
    task_id    TEXT NOT NULL REFERENCES tasks (id),
    depends_on TEXT NOT NULL REFERENCES tasks (id),
    PRIMARY KEY (task_id, depends_on),
    CHECK (task_id <> depends_on)
) STRICT, WITHOUT ROWID;

INSERT INTO task_deps (task_id, depends_on) SELECT task_id, depends_on FROM task_deps_copy;
DROP TABLE task_deps_copy;
CREATE INDEX task_deps_depends_on ON task_deps (depends_on);
