-- What the model gateway served (issue #20): one row per request, with the tokens the provider reported.
-- Only internal/controlplane/modelgateway reads and writes this table.

-- +goose Up
CREATE TABLE gateway_usage (
    id                TEXT    NOT NULL PRIMARY KEY,
    key_id            TEXT    NOT NULL REFERENCES gateway_keys (id),
    -- The public name asked for, and where the last attempt went.
    model             TEXT    NOT NULL,
    provider          TEXT    NOT NULL,
    upstream_model    TEXT    NOT NULL,
    stream            INTEGER NOT NULL CHECK (stream IN (0, 1)),
    -- How many targets were tried.
    attempts          INTEGER NOT NULL CHECK (attempts > 0),
    -- ok; upstream_error (the provider answered with an error); transport_error (no answer); truncated (a stream
    -- ended without its end mark); client_gone (the client left first).
    outcome           TEXT    NOT NULL
        CHECK (outcome IN ('ok', 'upstream_error', 'transport_error', 'truncated', 'client_gone')),
    -- The provider's HTTP status; 0 when it gave none.
    status            INTEGER NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens     INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
    -- 1 when the provider reported no usage and the counts are bytes divided by four, as 9router-go estimates.
    estimated         INTEGER NOT NULL DEFAULT 0 CHECK (estimated IN (0, 1)),
    duration_ms       INTEGER NOT NULL,
    created_at        INTEGER NOT NULL
) STRICT;

CREATE INDEX gateway_usage_created ON gateway_usage (created_at, id);

-- +goose Down
DROP INDEX gateway_usage_created;
DROP TABLE gateway_usage;
