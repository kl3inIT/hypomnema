-- The model gateway's model table and keys (issue #20), after LiteLLM's model_list, new-api's abilities and tokens,
-- and Bifrost's virtual keys. Only internal/controlplane/modelgateway reads and writes these tables.

-- +goose Up
-- A model the gateway serves under a public name. A name that is not here is refused.
CREATE TABLE gateway_models (
    name       TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    -- What the model does; each capability has its own endpoint and adapter interface.
    capability TEXT    NOT NULL CHECK (capability IN ('chat')),
    -- The model's YAML as applied.
    source     TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- Where a model is served from, in the order tried: a provider and the model's name there.
CREATE TABLE gateway_model_targets (
    model          TEXT    NOT NULL REFERENCES gateway_models (name) ON DELETE CASCADE,
    position       INTEGER NOT NULL CHECK (position >= 0),
    provider       TEXT    NOT NULL REFERENCES providers (name),
    upstream_model TEXT    NOT NULL CHECK (length(upstream_model) > 0),
    PRIMARY KEY (model, position)
) STRICT;

CREATE INDEX gateway_model_targets_provider ON gateway_model_targets (provider);

-- A key a client calls the gateway with: the coordinator, a tool or a machine of the owner.
CREATE TABLE gateway_keys (
    id         TEXT    NOT NULL PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    -- SHA-256 of the key, hex; the key itself is shown once and never stored.
    key_hash   TEXT    NOT NULL UNIQUE,
    -- The public model names the key may use, as a JSON array; ["*"] is every model.
    models     TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER
) STRICT;

-- +goose Down
DROP TABLE gateway_keys;
DROP INDEX gateway_model_targets_provider;
DROP TABLE gateway_model_targets;
DROP TABLE gateway_models;
