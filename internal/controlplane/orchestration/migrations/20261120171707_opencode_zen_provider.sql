-- OpenCode Zen as a built-in provider (issue #965), as opencode-go is (00120): the owner adds a Console key as an
-- account (`accounts add opencode_zen <label> --token -`) and the gateway serves the provider's models as
-- opencode-zen/<model>, paid from the workspace's balance. The models are the paid examples of OpenCode's inference
-- documentation (console/api/inference.mdx); the gateway gives the provider the models of its keys' Console lists
-- soon after it starts and every hour (vendorModelsLoop).

-- A provider the owner already named opencode-zen, of another kind, is left as it is and nothing is seeded (review of
-- PR #1000): a name is the owner's once taken, and an upgrade must not stop on it. Zen is then served under a
-- provider of kind opencode_zen the owner names (`providers apply`), which the hourly refresh fills as it fills this.

-- +goose Up
INSERT INTO providers (name, kind, source, created_at, updated_at)
SELECT 'opencode-zen', 'opencode_zen',
       'name: opencode-zen
kind: opencode_zen
models: [glm-5.3, kimi-k3, minimax-m3, claude-sonnet-5-5, gpt-6.1-sol]
', 0, 0
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE kind = 'opencode_zen' OR name = 'opencode-zen');

INSERT OR IGNORE INTO models (provider, name)
SELECT 'opencode-zen', m.name
FROM (SELECT 'glm-5.3' AS name UNION ALL SELECT 'kimi-k3' UNION ALL SELECT 'minimax-m3'
      UNION ALL SELECT 'claude-sonnet-5-5' UNION ALL SELECT 'gpt-6.1-sol') AS m
WHERE EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

-- +goose Down
-- The code before has no OpenCode Zen. What names this provider is left without it before the provider goes, so no
-- foreign key stops the way back and that code reads every row and refuses no Task for it (review of PR #1000, round
-- 3): a Task's Zen model is taken off, and it runs with its profile's; a profile version, a Bot or a chat on Zen
-- keeps no model, and runs with its default; a Bot's Zen fallbacks and the gateway's Zen targets go, and a gateway
-- model with no other target with them. Zen's accounts stay, which that code does not serve. Only Zen's own provider
-- is touched, never one the owner named opencode-zen (see Up).
UPDATE tasks SET model = NULL
WHERE substr(model, 1, 13) = 'opencode-zen/'
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

UPDATE profile_versions SET provider = NULL, model = NULL
WHERE provider = 'opencode-zen'
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

UPDATE bots SET provider = NULL, model = NULL
WHERE provider = 'opencode-zen'
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

UPDATE bots
SET fallbacks = coalesce((
    SELECT group_concat(value, ',') FROM json_each('["' || replace(bots.fallbacks, ',', '","') || '"]')
    WHERE substr(value, 1, 13) <> 'opencode-zen/'), '')
WHERE instr(fallbacks, 'opencode-zen/') > 0
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

UPDATE chats SET provider = NULL, model = NULL
WHERE provider = 'opencode-zen'
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

DELETE FROM gateway_models
WHERE name IN (SELECT model FROM gateway_model_targets WHERE provider = 'opencode-zen')
  AND NOT EXISTS (SELECT 1 FROM gateway_model_targets t WHERE t.model = gateway_models.name AND t.provider <> 'opencode-zen')
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

DELETE FROM gateway_model_targets
WHERE provider = 'opencode-zen'
  AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

DELETE FROM models
WHERE provider = 'opencode-zen' AND EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');
DELETE FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen';
