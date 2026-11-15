-- A model is chosen under its subscription and runs through the gateway, which serves each subscription's models
-- without a row in its table (issue #549). Bots, chats and Profile versions on a provider of kind model_gateway
-- move to the subscription and model of the gateway model they name when it has one target only, a subscription's
-- model: such a name is the subscription's model by another name. A name over several targets was made on purpose,
-- for its failover, and stays. A Profile version's YAML is changed the same way, as 00073 changed it; a version
-- whose YAML says its model in a form not matched here stays as it is, columns and text alike. Its digest is left:
-- applying the old YAML again keeps the moved version, which runs the same model.
--
-- Then what nothing uses any more goes: a gateway model of one target that a key that works does not list (a running
-- Dispatch's key keeps its model until it settles), and a provider of kind model_gateway that nothing names, with its
-- models (ON DELETE CASCADE). Data that cannot move is left as it is.

-- +goose Up
CREATE TEMP TABLE subscription_move AS
SELECT g.name AS model, t.provider AS provider, t.upstream_model AS upstream
FROM gateway_models g
JOIN gateway_model_targets t ON t.model = g.name
JOIN providers p ON p.name = t.provider
WHERE p.kind IN ('claude_subscription', 'codex_subscription', 'antigravity_subscription')
  AND (SELECT count(*) FROM gateway_model_targets WHERE model = g.name) = 1
  AND EXISTS (SELECT 1 FROM models m WHERE m.provider = t.provider AND m.name = t.upstream_model);

CREATE TEMP TABLE gateway_providers AS SELECT name FROM providers WHERE kind = 'model_gateway';

UPDATE bots
SET provider = (SELECT provider FROM subscription_move WHERE model = bots.model),
    model    = (SELECT upstream FROM subscription_move WHERE model = bots.model)
WHERE provider IN (SELECT name FROM gateway_providers) AND model IN (SELECT model FROM subscription_move);

UPDATE chats
SET provider = (SELECT provider FROM subscription_move WHERE model = chats.model),
    model    = (SELECT upstream FROM subscription_move WHERE model = chats.model)
WHERE provider IN (SELECT name FROM gateway_providers) AND model IN (SELECT model FROM subscription_move);

-- The model block in flow ({provider: X, name: M, ...}, keys in any order) or block form; a top-level "name:" is the
-- profile's and starts its line, so the model's name is matched after "{" or a space only.
CREATE TEMP TABLE version_move AS
SELECT v.id, v.provider AS old, s.provider, s.upstream,
    replace(replace(replace(replace(replace(replace(replace(replace(v.source,
        'provider: ' || v.provider || ',', 'provider: ' || s.provider || ','),
        'provider: ' || v.provider || '}', 'provider: ' || s.provider || '}'),
        'provider: ' || v.provider || char(10), 'provider: ' || s.provider || char(10)),
        '{name: ' || v.model || ',', '{name: ' || s.upstream || ','),
        '{name: ' || v.model || '}', '{name: ' || s.upstream || '}'),
        ' name: ' || v.model || ',', ' name: ' || s.upstream || ','),
        ' name: ' || v.model || '}', ' name: ' || s.upstream || '}'),
        ' name: ' || v.model || char(10), ' name: ' || s.upstream || char(10)) AS source
FROM profile_versions v
JOIN subscription_move s ON s.model = v.model
WHERE v.provider IN (SELECT name FROM gateway_providers);

DELETE FROM version_move
WHERE instr(source, 'provider: ' || old) > 0
   OR instr(source, 'provider: ' || provider) = 0
   OR instr(source, 'name: ' || upstream) = 0;

UPDATE profile_versions
SET source   = (SELECT source FROM version_move m WHERE m.id = profile_versions.id),
    provider = (SELECT provider FROM version_move m WHERE m.id = profile_versions.id),
    model    = (SELECT upstream FROM version_move m WHERE m.id = profile_versions.id)
WHERE id IN (SELECT id FROM version_move);

-- A starting profile's version as 00081 seeded it gets the digest of its YAML as now written, as 00081 wrote its own,
-- so applying that YAML again makes no new version.
UPDATE profile_versions
SET digest = CASE digest
    WHEN '961b7c708025e6cb22727267575e64492271dec3f11936be8cbf78f47000cbf2'
        THEN 'b8c9bcff13f99335ef27524fd18f1d0d5e90f5ca1430e6635e27e3f398e6ec9a'
    WHEN '3fe47b47806540c1b4d2233b12510285bc641a47c357ece48791e178311b5413'
        THEN 'f1ffca4e6b3249f79898deae3d9acd6d7c29f8ed3cbc424ef263f537835098d0'
    WHEN '0ee2609075fa724167c37aafde8e853cd2c6caaaede51172680b42d36ee16dfc'
        THEN '7c154c9252a95c4ae18bb5b7fcf27d3b8c96aafeca10a7a6ee14157b7efb142a'
    END
WHERE id IN ('pfv_seed3_implementer', 'pfv_seed4_reviewer', 'pfv_seed4_explorer')
  AND provider = 'claude-subscription'
  AND digest IN ('961b7c708025e6cb22727267575e64492271dec3f11936be8cbf78f47000cbf2',
                 '3fe47b47806540c1b4d2233b12510285bc641a47c357ece48791e178311b5413',
                 '0ee2609075fa724167c37aafde8e853cd2c6caaaede51172680b42d36ee16dfc');

DELETE FROM gateway_models
WHERE name IN (SELECT model FROM subscription_move)
  AND (SELECT count(*) FROM gateway_model_targets WHERE model = gateway_models.name) = 1
  AND NOT EXISTS (SELECT 1 FROM profile_versions v WHERE v.provider IN (SELECT name FROM gateway_providers)
                  AND v.model = gateway_models.name)
  AND NOT EXISTS (SELECT 1 FROM bots b WHERE b.provider IN (SELECT name FROM gateway_providers)
                  AND b.model = gateway_models.name)
  AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.provider IN (SELECT name FROM gateway_providers)
                  AND c.model = gateway_models.name)
  AND NOT EXISTS (SELECT 1 FROM gateway_keys k, json_each(k.models) j
                  WHERE k.revoked_at IS NULL AND j.value = gateway_models.name);

DELETE FROM providers
WHERE name IN (SELECT name FROM gateway_providers)
  AND NOT EXISTS (SELECT 1 FROM profile_versions WHERE provider = providers.name)
  AND NOT EXISTS (SELECT 1 FROM bots WHERE provider = providers.name)
  AND NOT EXISTS (SELECT 1 FROM chats WHERE provider = providers.name);

DROP TABLE version_move;
DROP TABLE subscription_move;
DROP TABLE gateway_providers;

-- +goose Down
-- What moved cannot be told apart from what was chosen under a subscription since; it stays where it is, and the
-- gateway still serves it under the subscription's name.
SELECT 1;
