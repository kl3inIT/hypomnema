-- The part of a request's cache write that lasts an hour (issue #448): Anthropic's
-- usage.cache_creation.ephemeral_1h_input_tokens, part of cache_write_tokens. It costs twice the input where a
-- five-minute write costs 1.25 times (LiteLLM's cache_creation_input_token_cost_above_1hr; Coder's aibridge reads the
-- same count). Rows recorded before keep 0 and their cost; nothing is recomputed.

-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN cache_write_1h_tokens;
