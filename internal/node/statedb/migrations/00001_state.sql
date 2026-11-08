-- The node's own state (issue #32): the Dispatches it holds and the reports not yet acknowledged, as Omnara's
-- statedb (internal/machinedaemon/statedb at 0ef1711).

-- +goose Up
CREATE TABLE dispatches (
    id          TEXT    NOT NULL PRIMARY KEY,
    -- offered: accept sent, not yet acknowledged; accepted: the worker may run; ended: its final report is queued.
    state       TEXT    NOT NULL CHECK (state IN ('offered', 'accepted', 'ended')),
    epoch       INTEGER NOT NULL DEFAULT 0,
    -- The offer as JSON (nodeproto.Offer), and the accept response (nodeproto.Accepted) once there is one.
    offer       TEXT    NOT NULL,
    accepted    TEXT,
    process_ref TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE outbox (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    report_id  TEXT    NOT NULL UNIQUE,
    -- The report as JSON (nodeproto.Report).
    report     TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE outbox;
DROP TABLE dispatches;
