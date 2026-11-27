-- The owner's GitHub App (issue #1262). Its credential (App id, private key, webhook secret) is a secret in the vault,
-- named github-app; these tables hold what is not secret.

-- +goose Up
-- The repositories the App is installed on, each with its installation and GitHub's id of it, read from GitHub after
-- the owner installs it and on each installation event: a worker on one of them gets a token of the installation for
-- that repository alone (repository_ids) instead of the owner's.
CREATE TABLE github_app_installations (
    repository      TEXT    NOT NULL PRIMARY KEY
        CHECK (repository = lower(repository) AND instr(repository, '/') > 1),
    repository_id   INTEGER NOT NULL CHECK (repository_id > 0),
    installation_id INTEGER NOT NULL CHECK (installation_id > 0),
    account         TEXT    NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;

-- Each delivery the App was sent, by GitHub's id, so one delivered again is handled once (Omnara keeps its deliveries
-- by id, executionstore/event_webhook_deliveries_store.go). The body of a conversation's event (issue_comment and the
-- reviews) is kept for issue #1232, which answers them; no other body is.
CREATE TABLE github_deliveries (
    id          TEXT    NOT NULL PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
    event       TEXT    NOT NULL CHECK (length(event) BETWEEN 1 AND 128),
    action      TEXT    NOT NULL DEFAULT '',
    repository  TEXT    NOT NULL DEFAULT '',
    payload     TEXT,
    received_at INTEGER NOT NULL
) STRICT;

CREATE INDEX github_deliveries_by_time ON github_deliveries (received_at);

-- The CI of a pull request someone waits for (`hypomnema ci wait`): its head, and whether the head's required checks
-- passed, failed or are still pending, or the request merged or closed. A check_suite or workflow_run that completes
-- on its head, or a pull_request that closes it, changes it; read_at is when GitHub was last read for it.
CREATE TABLE pull_request_ci (
    repository TEXT    NOT NULL CHECK (repository = lower(repository) AND instr(repository, '/') > 1),
    number     INTEGER NOT NULL CHECK (number > 0),
    head       TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'passed', 'failed', 'merged', 'closed')),
    failed     TEXT    NOT NULL DEFAULT '',
    read_at    INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (repository, number)
) STRICT;

-- +goose Down
DROP TABLE pull_request_ci;
DROP INDEX github_deliveries_by_time;
DROP TABLE github_deliveries;
DROP TABLE github_app_installations;
