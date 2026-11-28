-- +goose Up
-- The repositories whose work a Bot owns (issue #1334): a Run created on one goes to that Bot's chat, as Paperclip's
-- project names its lead agent (packages/db/src/schema/projects.ts, lead_agent_id). A repository has at most one
-- owning Bot, as a Paperclip issue has one assignee (docs/api/issues.md, checkout 409): the key is the repository.
-- The main Bot holds none and takes the Runs of a repository no Bot owns. Removing a Bot frees its repositories.
-- Its own table, so a Bot's row and its settings stay as they are.
CREATE TABLE bot_repos (
    -- The repository as a Run names it (runs.repo_host, runs.repo): its host, and its path there, owner/name on
    -- github.com. Hosts compare paths without case, so two spellings of one repository are one claim.
    host       TEXT    NOT NULL CHECK (length(host) > 0),
    path       TEXT    NOT NULL COLLATE NOCASE CHECK (length(path) > 0),
    bot_id     TEXT    NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (host, path)
) STRICT;

CREATE INDEX bot_repos_bot ON bot_repos (bot_id);

-- +goose Down
DROP TABLE bot_repos;
