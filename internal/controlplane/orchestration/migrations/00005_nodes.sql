-- Nodes, accounts and placement (issue #19, PR 5). A node is registered with a one-time token and reports
-- its CLIs; accounts are managed here, with no secret value; a rate limit cools an account on every node.

-- +goose Up
CREATE TABLE nodes (
    id             TEXT    NOT NULL PRIMARY KEY,
    name           TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    -- SHA-256 of the node's token, hex; the token itself is shown once and never stored.
    token_hash     TEXT    NOT NULL UNIQUE,
    status         TEXT    NOT NULL CHECK (status IN ('active', 'drained')),
    max_dispatches INTEGER NOT NULL CHECK (max_dispatches > 0),
    -- Reported by the node at each connect: where its worktrees live, and its CLIs as JSON.
    worktree_root  TEXT,
    runtimes       TEXT    NOT NULL DEFAULT '[]',
    last_seen_at   INTEGER,
    -- Set when the node let Dispatches expire in a row; cleared by its next hello or a resume.
    unavailable_at INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;

CREATE TABLE accounts (
    id         TEXT    NOT NULL PRIMARY KEY,
    runtime    TEXT    NOT NULL CHECK (length(runtime) > 0),
    label      TEXT    NOT NULL CHECK (length(label) > 0),
    status     TEXT    NOT NULL CHECK (status IN ('active', 'disabled')),
    -- Placement takes the first active account of a runtime, by position.
    position   INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (runtime, label)
) STRICT;

CREATE TABLE account_cooldowns (
    runtime     TEXT    NOT NULL,
    account_ref TEXT    NOT NULL,
    until       INTEGER NOT NULL,
    reason      TEXT    NOT NULL,
    PRIMARY KEY (runtime, account_ref)
) STRICT, WITHOUT ROWID;

INSERT INTO account_cooldowns (runtime, account_ref, until, reason)
SELECT runtime, account_ref, max(until), reason FROM slot_cooldowns GROUP BY runtime, account_ref;

DROP TABLE slot_cooldowns;

-- The model and effort the Dispatch was asked to run with: the Task's own, or its role's default.
ALTER TABLE dispatches ADD COLUMN model TEXT;
ALTER TABLE dispatches ADD COLUMN effort TEXT;

-- +goose Down
ALTER TABLE dispatches DROP COLUMN effort;
ALTER TABLE dispatches DROP COLUMN model;

CREATE TABLE slot_cooldowns (
    node_id     TEXT    NOT NULL,
    runtime     TEXT    NOT NULL,
    account_ref TEXT    NOT NULL,
    until       INTEGER NOT NULL,
    reason      TEXT    NOT NULL,
    PRIMARY KEY (node_id, runtime, account_ref)
) STRICT, WITHOUT ROWID;

DROP TABLE account_cooldowns;
DROP TABLE accounts;
DROP TABLE nodes;
