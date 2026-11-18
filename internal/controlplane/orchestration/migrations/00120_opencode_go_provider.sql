-- OpenCode Go as a built-in provider (issue #854), as antigravity-subscription is: the owner adds a Go key as an
-- account (`accounts add opencode_go <label> --token -`) and the gateway serves the provider's models as
-- opencode-go/<model>. The models are some of those Go listed at GET https://opencode.ai/inference/go/v1/models on
-- 2026-11-18; the gateway sets the provider to Go's list soon after it starts and every hour (vendorModelsLoop).

-- +goose Up
INSERT INTO providers (name, kind, source, created_at, updated_at)
SELECT 'opencode-go', 'opencode_go',
       'name: opencode-go
kind: opencode_go
models: [glm-5.3, kimi-k3, deepseek-v4-pro, minimax-m3, qwen3.8-max, mimo-v2.6-pro]
', 0, 0
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE kind = 'opencode_go');

INSERT OR IGNORE INTO models (provider, name)
SELECT 'opencode-go', m.name
FROM (SELECT 'glm-5.3' AS name UNION ALL SELECT 'kimi-k3' UNION ALL SELECT 'deepseek-v4-pro'
      UNION ALL SELECT 'minimax-m3' UNION ALL SELECT 'qwen3.8-max' UNION ALL SELECT 'mimo-v2.6-pro') AS m
WHERE EXISTS (SELECT 1 FROM providers WHERE name = 'opencode-go' AND kind = 'opencode_go');

-- +goose Down
DELETE FROM models WHERE provider = 'opencode-go';
DELETE FROM providers WHERE name = 'opencode-go' AND kind = 'opencode_go';
