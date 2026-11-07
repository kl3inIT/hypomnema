-- Runs, Tasks and their dependencies (issue #19, PR 1). Times are Unix milliseconds
-- from the injected clock. Status values and allowed transitions are listed in
-- transitions.go; the CHECKs here only keep the columns within those values.

-- +goose Up
CREATE TABLE runs (
    id          TEXT    NOT NULL PRIMARY KEY,
    objective   TEXT    NOT NULL CHECK (length(objective) > 0),
    status      TEXT    NOT NULL CHECK (status IN ('open', 'finished', 'canceled')),
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    finished_at INTEGER
) STRICT;

CREATE TABLE tasks (
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

CREATE INDEX tasks_run_status ON tasks (run_id, status);

CREATE TABLE task_deps (
    task_id    TEXT NOT NULL REFERENCES tasks (id),
    depends_on TEXT NOT NULL REFERENCES tasks (id),
    PRIMARY KEY (task_id, depends_on),
    CHECK (task_id <> depends_on)
) STRICT, WITHOUT ROWID;

CREATE INDEX task_deps_depends_on ON task_deps (depends_on);

-- +goose Down
DROP TABLE task_deps;
DROP TABLE tasks;
DROP TABLE runs;
