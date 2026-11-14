-- The provider pi was given before the model gateway had a kind of its own (issue #413): one named `gateway`, of
-- kind openai_compatible, at the control plane's own /v1 on this machine. Its Bots, chats and Profiles move to the
-- provider of kind model_gateway (the first by name), and it goes, with its models (ON DELETE CASCADE). Nothing is
-- created: without a model_gateway provider, or when a model in use is not one that provider offers, or when a
-- gateway model is served through the old provider, the data is left as it is.

-- +goose Up
CREATE TEMP TABLE gateway_move AS
SELECT p.name AS old, (SELECT name FROM providers WHERE kind = 'model_gateway' ORDER BY name LIMIT 1) AS new
FROM providers p
WHERE p.name = 'gateway' AND p.kind = 'openai_compatible'
  AND rtrim(p.base_url, '/') LIKE '%/v1'
  AND (p.base_url LIKE 'http://127.0.0.1:%' OR p.base_url LIKE 'http://127.0.0.1/%'
    OR p.base_url LIKE 'http://localhost:%' OR p.base_url LIKE 'http://localhost/%');

DELETE FROM gateway_move
WHERE new IS NULL
   OR EXISTS (SELECT 1 FROM gateway_model_targets t WHERE t.provider = gateway_move.old)
   OR EXISTS (
       SELECT 1 FROM bots b
       WHERE b.provider = gateway_move.old AND coalesce(b.model, '') <> ''
         AND NOT EXISTS (SELECT 1 FROM models m WHERE m.provider = gateway_move.new AND m.name = b.model))
   OR EXISTS (
       SELECT 1 FROM chats c
       WHERE c.provider = gateway_move.old AND coalesce(c.model, '') <> ''
         AND NOT EXISTS (SELECT 1 FROM models m WHERE m.provider = gateway_move.new AND m.name = c.model))
   OR EXISTS (
       SELECT 1 FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id
       WHERE v.provider = gateway_move.old AND coalesce(v.model, '') <> ''
         AND NOT EXISTS (SELECT 1 FROM models m WHERE m.provider = gateway_move.new AND m.name = v.model));

UPDATE bots SET provider = (SELECT new FROM gateway_move) WHERE provider IN (SELECT old FROM gateway_move);
UPDATE chats SET provider = (SELECT new FROM gateway_move) WHERE provider IN (SELECT old FROM gateway_move);

-- A Profile names its provider in its YAML too; the digest of an edited version is left, so applying the old YAML
-- again makes a new version.
UPDATE profile_versions
SET provider = (SELECT new FROM gateway_move),
    source = replace(source, 'provider: gateway' || char(10), 'provider: ' || (SELECT new FROM gateway_move) || char(10))
WHERE provider IN (SELECT old FROM gateway_move);

DELETE FROM providers WHERE name IN (SELECT old FROM gateway_move);

DROP TABLE gateway_move;

-- +goose Down
-- The old provider is not put back: its YAML, address, key reference and models are gone, and which Bots, chats and
-- Profiles were on it cannot be told apart from those on the model_gateway provider. They stay where they are.
SELECT 1;
