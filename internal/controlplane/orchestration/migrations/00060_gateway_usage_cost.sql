-- The cost of what the gateway served (issue #304): the tokens written to the provider's cache, which cost more than
-- input (Coder's cache_write_input_tokens), and the cost at the vendor's API price with the model it was priced as,
-- taken from the price book when the request is recorded (Coder's aibridge_token_usages cost_micros and
-- priced_model). The cost is NULL when the price book has no price for the model. For a subscription it is what the
-- same tokens would cost at the API, not what the plan costs.

-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_usage ADD COLUMN cost_micros INTEGER;
ALTER TABLE gateway_usage ADD COLUMN priced_model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN priced_model;
ALTER TABLE gateway_usage DROP COLUMN cost_micros;
ALTER TABLE gateway_usage DROP COLUMN cache_write_tokens;
