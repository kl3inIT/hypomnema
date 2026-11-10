-- +goose Up
-- A Connection is one authorized account of an Integration (issue #103), with the label the owner gave it. Its
-- credential is a vault secret, named here and never held here. A git host's one token (issue #44) becomes that
-- host's Connection "default"; a host is an Integration whose id is its name.
CREATE TABLE connections (
    integration TEXT NOT NULL,
    label       TEXT NOT NULL,
    -- The account as the service names it (a login, an email), when known.
    account     TEXT,
    secret      TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (integration, label)
) STRICT;

INSERT INTO connections (integration, label, account, secret, created_at, updated_at)
SELECT host, 'default', NULL, token_secret, created_at, created_at FROM git_hosts WHERE token_secret IS NOT NULL;

ALTER TABLE git_hosts DROP COLUMN token_secret;

-- The Connection work on a registered repository uses; NULL when its host has one, or none.
ALTER TABLE repos ADD COLUMN connection TEXT;

-- +goose Down
ALTER TABLE repos DROP COLUMN connection;

ALTER TABLE git_hosts ADD COLUMN token_secret TEXT;

UPDATE git_hosts
SET token_secret = (SELECT c.secret FROM connections c WHERE c.integration = git_hosts.host AND c.label = 'default');

DROP TABLE connections;
