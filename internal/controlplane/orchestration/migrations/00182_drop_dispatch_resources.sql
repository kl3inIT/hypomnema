-- What a Dispatch holds on its node was to be reported (issue #28), but no node ever sent it, and the table is empty
-- on the live database (issue #1220, 2026-11-27): it goes with the calls that wrote it.

-- +goose Up
DROP TABLE dispatch_resources;

-- +goose Down
CREATE TABLE dispatch_resources (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    kind        TEXT    NOT NULL CHECK (kind IN ('worktree', 'vault_session', 'process')),
    ref         TEXT    NOT NULL CHECK (length(ref) > 0),
    custody     TEXT    NOT NULL CHECK (custody IN ('owned', 'external')),
    created_at  INTEGER NOT NULL,
    released_at INTEGER,
    PRIMARY KEY (dispatch_id, kind, ref)
) STRICT, WITHOUT ROWID;
