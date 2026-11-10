-- Git hosts and repositories (issue #44). A Run's repository is a host and a path: github.com with owner/name as
-- before, gitlab.com, or a company server the owner declared. A repository can be registered under a name of
-- the owner's choosing, with the branch its worktrees start from.

-- +goose Up

-- NULL means github.com: every Run from before hosts existed.
ALTER TABLE runs ADD COLUMN repo_host TEXT;

-- The hosts the owner declared. github.com and gitlab.com need no row.
CREATE TABLE git_hosts (
    host       TEXT    NOT NULL PRIMARY KEY CHECK (length(host) > 0),
    kind       TEXT    NOT NULL CHECK (kind IN ('github', 'gitlab')),
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE repos (
    name       TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    host       TEXT    NOT NULL CHECK (length(host) > 0),
    path       TEXT    NOT NULL CHECK (length(path) > 0),
    -- The branch worktrees start from when a Run names none.
    base       TEXT,
    created_at INTEGER NOT NULL,
    UNIQUE (host, path)
) STRICT;

-- +goose Down
DROP TABLE repos;
DROP TABLE git_hosts;
ALTER TABLE runs DROP COLUMN repo_host;
