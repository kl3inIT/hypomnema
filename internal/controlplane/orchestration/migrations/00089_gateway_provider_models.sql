-- A provider of kind model_gateway offers the gateway's chat models, read from gateway_models (issue #537); the list
-- typed in its YAML goes. On zm it had drifted: 4 typed against 6 served.
--
-- Its models rows go, and its YAML is written again without the list: name and kind, with base_url and api_key_ref
-- where it has them, so applying it again is accepted.

-- +goose Up
DELETE FROM models
WHERE provider IN (SELECT name FROM providers WHERE kind = 'model_gateway');

UPDATE providers
SET source = 'name: ' || name || char(10) || 'kind: ' || kind || char(10)
    || coalesce('base_url: ' || base_url || char(10), '')
    || coalesce('api_key_ref: ' || api_key_ref || char(10), '')
WHERE kind = 'model_gateway';

-- +goose Down
-- The typed list comes back as the gateway's chat models were then.
INSERT OR IGNORE INTO models (provider, name)
SELECT p.name, g.name
FROM providers p JOIN gateway_models g ON g.capability = 'chat'
WHERE p.kind = 'model_gateway';
