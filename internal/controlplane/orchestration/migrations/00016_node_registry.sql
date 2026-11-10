-- The machine registry (issue #69, step 1a). A node has several tokens, each revocable, in their own table
-- (Omnara's machine_daemon_tokens); a removed node stays as `deleted` and frees its name; and a node reports its
-- version, platform and size at each connect (Omnara's observed_platform, Nezha's Host).

-- +goose Up
CREATE TABLE node_token_seed (
    node_id    TEXT    NOT NULL,
    token_hash TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

INSERT INTO node_token_seed (node_id, token_hash, created_at) SELECT id, token_hash, created_at FROM nodes;

CREATE TABLE nodes_new (
    id             TEXT    NOT NULL PRIMARY KEY,
    name           TEXT    NOT NULL CHECK (length(name) > 0),
    -- active takes work, drained finishes what it has, deleted is gone and only kept for the record.
    status         TEXT    NOT NULL CHECK (status IN ('active', 'drained', 'deleted')),
    max_dispatches INTEGER NOT NULL CHECK (max_dispatches > 0),
    -- Reported by the node at each connect: where its worktrees live, its CLIs as JSON, and what it is.
    worktree_root  TEXT,
    runtimes       TEXT    NOT NULL DEFAULT '[]',
    daemon_version TEXT,
    os             TEXT,
    arch           TEXT,
    cpus           INTEGER,
    memory_bytes   INTEGER,
    -- The size of the volume that holds the worktree root.
    disk_bytes     INTEGER,
    last_seen_at   INTEGER,
    -- Set when the node let Dispatches expire in a row; cleared by its next hello or a resume.
    unavailable_at INTEGER,
    deleted_at     INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    CHECK ((status = 'deleted') = (deleted_at IS NOT NULL))
) STRICT;

INSERT INTO nodes_new (id, name, status, max_dispatches, worktree_root, runtimes, last_seen_at, unavailable_at,
                       created_at, updated_at)
SELECT id, name, status, max_dispatches, worktree_root, runtimes, last_seen_at, unavailable_at, created_at, updated_at
FROM nodes;

DROP TABLE nodes;
ALTER TABLE nodes_new RENAME TO nodes;

-- A removed node's name can be given to a new one.
CREATE UNIQUE INDEX nodes_live_name ON nodes (name) WHERE status <> 'deleted';

CREATE TABLE node_tokens (
    id           TEXT    NOT NULL PRIMARY KEY,
    node_id      TEXT    NOT NULL REFERENCES nodes (id),
    name         TEXT    NOT NULL CHECK (length(name) > 0),
    -- SHA-256 of the token, hex; the token itself is shown once and never stored.
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    -- Written at most once an hour.
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX node_tokens_node ON node_tokens (node_id, created_at, id);

INSERT INTO node_tokens (id, node_id, name, token_hash, created_at)
SELECT 'ntk_' || node_id, node_id, 'first', token_hash, created_at FROM node_token_seed;

DROP TABLE node_token_seed;

-- +goose Down
CREATE TABLE nodes_old (
    id             TEXT    NOT NULL PRIMARY KEY,
    name           TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    token_hash     TEXT    NOT NULL UNIQUE,
    status         TEXT    NOT NULL CHECK (status IN ('active', 'drained')),
    max_dispatches INTEGER NOT NULL CHECK (max_dispatches > 0),
    worktree_root  TEXT,
    runtimes       TEXT    NOT NULL DEFAULT '[]',
    last_seen_at   INTEGER,
    unavailable_at INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;

-- A node keeps its oldest token that still works; one with none gets a hash no token has. Removed nodes go.
INSERT INTO nodes_old (id, name, token_hash, status, max_dispatches, worktree_root, runtimes, last_seen_at,
                       unavailable_at, created_at, updated_at)
SELECT n.id, n.name,
       coalesce((SELECT t.token_hash FROM node_tokens t
                 WHERE t.node_id = n.id AND t.revoked_at IS NULL
                 ORDER BY t.created_at, t.id LIMIT 1), 'none:' || n.id),
       n.status, n.max_dispatches, n.worktree_root, n.runtimes, n.last_seen_at, n.unavailable_at, n.created_at,
       n.updated_at
FROM nodes n WHERE n.status <> 'deleted';

DROP TABLE node_tokens;
DROP TABLE nodes;
ALTER TABLE nodes_old RENAME TO nodes;
