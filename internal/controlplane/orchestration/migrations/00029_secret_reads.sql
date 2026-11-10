-- +goose Up
-- Each time a secret's value is read by the owner (issue #108): the secret's name and the token that read it,
-- never the value. It is its own table, as Infisical's audit log is apart from its other records: the event log
-- is read by every token, a coordinator's included, and what the owner looked at is not theirs to see.
CREATE TABLE secret_reads (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    secret     TEXT    NOT NULL,
    -- The token that read it, or "host" for the command run on the control plane host.
    token_id   TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX secret_reads_secret ON secret_reads (secret, id);

-- +goose Down
DROP TABLE secret_reads;
