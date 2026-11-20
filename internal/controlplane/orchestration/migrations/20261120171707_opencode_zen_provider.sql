-- OpenCode Zen as a built-in provider (issue #965), as opencode-go is (00120): the owner adds a Console key as an
-- account (`accounts add opencode_zen <label> --token -`) and the gateway serves the provider's models as
-- opencode-zen/<model>, paid from the workspace's balance. The models are the paid examples of OpenCode's inference
-- documentation (console/api/inference.mdx); the gateway sets the provider to the workspace's Console list soon after
-- it starts and every hour (vendorModelsLoop).

-- +goose Up
INSERT INTO providers (name, kind, source, created_at, updated_at)
SELECT 'opencode-zen', 'opencode_zen',
       'name: opencode-zen
kind: opencode_zen
models: [glm-5.3, kimi-k3, minimax-m3, claude-sonnet-5-5, gpt-6.1-sol]
', 0, 0
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE kind = 'opencode_zen');

INSERT OR IGNORE INTO models (provider, name)
SELECT 'opencode-zen', m.name
FROM (SELECT 'glm-5.3' AS name UNION ALL SELECT 'kimi-k3' UNION ALL SELECT 'minimax-m3'
      UNION ALL SELECT 'claude-sonnet-5-5' UNION ALL SELECT 'gpt-6.1-sol') AS m
WHERE EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen');

-- +goose Down
DELETE FROM models WHERE provider = 'opencode-zen';
DELETE FROM providers WHERE name = 'opencode-zen' AND kind = 'opencode_zen';
