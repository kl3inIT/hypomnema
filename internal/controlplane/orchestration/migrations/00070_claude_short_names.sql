-- Claude Code's short names leave the Claude subscription (issue #410; owner, 2026-11-14). 00008 seeded opus,
-- sonnet and haiku and 00043 fable beside the full models they duplicate; only the full names stay.
--
-- The provider's models rows go, and its YAML's models list loses them where it is a flow list as 00008 wrote it
-- (`[a, b]`), so applying it again does not bring them back. A gateway model of one of these names served from the
-- subscription goes with its targets (ON DELETE CASCADE), and gateway keys lose it from the models they may use. A
-- gateway model of another name whose target is a short name is pointed at the full model it stood for, the newest
-- of its family. gateway_usage keeps its rows: they name the model as it was asked for, with no foreign key.

-- +goose Up
DELETE FROM models
WHERE provider = 'claude-subscription' AND name IN ('opus', 'sonnet', 'haiku', 'fable');

UPDATE providers
SET source =
        replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(source,
            ', opus,', ','), ', opus]', ']'), '[opus, ', '['),
            ', sonnet,', ','), ', sonnet]', ']'), '[sonnet, ', '['),
            ', haiku,', ','), ', haiku]', ']'), '[haiku, ', '['),
            ', fable,', ','), ', fable]', ']'), '[fable, ', '[')
WHERE name = 'claude-subscription';

DELETE FROM gateway_models
WHERE name IN ('opus', 'sonnet', 'haiku', 'fable')
  AND name IN (SELECT model FROM gateway_model_targets WHERE provider = 'claude-subscription');

UPDATE gateway_model_targets
SET upstream_model = CASE upstream_model
                         WHEN 'opus' THEN 'claude-opus-5-5'
                         WHEN 'sonnet' THEN 'claude-sonnet-5-5'
                         WHEN 'haiku' THEN 'claude-haiku-5-5'
                         WHEN 'fable' THEN 'claude-fable-5-2'
    END
WHERE provider = 'claude-subscription' AND upstream_model IN ('opus', 'sonnet', 'haiku', 'fable');

UPDATE gateway_keys
SET models = (SELECT json_group_array(value)
              FROM (SELECT value FROM json_each(gateway_keys.models)
                    WHERE value NOT IN ('opus', 'sonnet', 'haiku', 'fable')
                       OR value IN (SELECT name FROM gateway_models)
                    ORDER BY key))
WHERE EXISTS (SELECT 1 FROM json_each(gateway_keys.models)
              WHERE value IN ('opus', 'sonnet', 'haiku', 'fable')
                AND value NOT IN (SELECT name FROM gateway_models));

-- +goose Down
-- What 00008 and 00043 seeded comes back: the models rows and 00008's list. The gateway models and keys' lists the
-- owner made are not kept anywhere, so they stay as Up left them.
INSERT OR IGNORE INTO models (provider, name)
SELECT 'claude-subscription', short.name
FROM (SELECT 'opus' AS name UNION ALL SELECT 'sonnet' UNION ALL SELECT 'haiku' UNION ALL SELECT 'fable') AS short
WHERE EXISTS (SELECT 1 FROM providers WHERE name = 'claude-subscription');

UPDATE providers
SET source = replace(source, 'models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5]',
                     'models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, opus, sonnet, haiku]')
WHERE name = 'claude-subscription';
