-- The provider `ninerouter` (issue #413, owner 2026-11-14): 9router on the control plane's machine, which the
-- gateway served gpt-6.1-sol through until 2026-11-10 and nothing has used since. It goes with its models (ON DELETE
-- CASCADE) when it is OpenAI-compatible at 9router's port and nothing names it: no Bot, chat, Profile version or
-- gateway model target. Its key stays in the vault as the secret `ninerouter`, for the owner to remove there.

-- +goose Up
DELETE FROM providers
WHERE name = 'ninerouter' AND kind = 'openai_compatible' AND base_url LIKE '%:20128/%'
  AND NOT EXISTS (SELECT 1 FROM bots WHERE provider = 'ninerouter')
  AND NOT EXISTS (SELECT 1 FROM chats WHERE provider = 'ninerouter')
  AND NOT EXISTS (SELECT 1 FROM profile_versions WHERE provider = 'ninerouter')
  AND NOT EXISTS (SELECT 1 FROM gateway_model_targets WHERE provider = 'ninerouter');

-- +goose Down
-- The provider is not put back: its YAML, address and models are gone.
SELECT 1;
