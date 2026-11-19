-- +goose Up
CREATE TABLE gateway_model_cooling (
    route TEXT NOT NULL,
    model TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    resets_at INTEGER NOT NULL,
    PRIMARY KEY (route, model)
) STRICT;

ALTER TABLE gateway_usage_daily ADD COLUMN no_account INTEGER NOT NULL DEFAULT 0;

ALTER TABLE gateway_usage RENAME TO gateway_usage_old;
CREATE TABLE gateway_usage (
    id TEXT NOT NULL PRIMARY KEY,
    key_id TEXT NOT NULL REFERENCES gateway_keys (id),
    model TEXT NOT NULL,
    provider TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    stream INTEGER NOT NULL CHECK (stream IN (0, 1)),
    attempts INTEGER NOT NULL CHECK (attempts > 0),
    outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'upstream_error', 'transport_error', 'truncated', 'client_gone', 'no_account')),
    status INTEGER NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    estimated INTEGER NOT NULL DEFAULT 0 CHECK (estimated IN (0, 1)),
    duration_ms INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    account_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_micros INTEGER,
    priced_model TEXT NOT NULL DEFAULT '',
    cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
    audio_ms INTEGER NOT NULL DEFAULT 0,
    characters INTEGER NOT NULL DEFAULT 0,
    web_searches INTEGER NOT NULL DEFAULT 0,
    images INTEGER NOT NULL DEFAULT 0,
    ttft_ms INTEGER
) STRICT;
INSERT INTO gateway_usage SELECT * FROM gateway_usage_old;
DROP TABLE gateway_usage_old;
CREATE INDEX gateway_usage_created ON gateway_usage (created_at, id);
CREATE INDEX gateway_usage_key ON gateway_usage (key_id);

-- +goose Down
DROP TABLE gateway_model_cooling;
UPDATE gateway_usage_daily SET requests = requests - no_account WHERE no_account > 0;
ALTER TABLE gateway_usage_daily DROP COLUMN no_account;

ALTER TABLE gateway_usage RENAME TO gateway_usage_new;
CREATE TABLE gateway_usage (
    id TEXT NOT NULL PRIMARY KEY,
    key_id TEXT NOT NULL REFERENCES gateway_keys (id),
    model TEXT NOT NULL,
    provider TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    stream INTEGER NOT NULL CHECK (stream IN (0, 1)),
    attempts INTEGER NOT NULL CHECK (attempts > 0),
    outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'upstream_error', 'transport_error', 'truncated', 'client_gone')),
    status INTEGER NOT NULL,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    estimated INTEGER NOT NULL DEFAULT 0 CHECK (estimated IN (0, 1)),
    duration_ms INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    account_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_micros INTEGER,
    priced_model TEXT NOT NULL DEFAULT '',
    cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
    audio_ms INTEGER NOT NULL DEFAULT 0,
    characters INTEGER NOT NULL DEFAULT 0,
    web_searches INTEGER NOT NULL DEFAULT 0,
    images INTEGER NOT NULL DEFAULT 0,
    ttft_ms INTEGER
) STRICT;
INSERT INTO gateway_usage SELECT * FROM gateway_usage_new WHERE outcome != 'no_account';
DROP TABLE gateway_usage_new;
CREATE INDEX gateway_usage_created ON gateway_usage (created_at, id);
CREATE INDEX gateway_usage_key ON gateway_usage (key_id);
