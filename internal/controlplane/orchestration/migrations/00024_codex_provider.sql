-- The Codex subscription as a built-in provider (issue #20, step 3), as claude-subscription is: the owner logs an
-- account in and the provider is there, with the chat models a ChatGPT login is served. The gateway serves only
-- the ones the owner puts in its model table.

-- +goose Up

INSERT INTO providers (name, kind, source, created_at, updated_at)
SELECT 'codex-subscription', 'codex_subscription',
       'name: codex-subscription
kind: codex_subscription
models: [gpt-6.1-sol, gpt-6-sol, gpt-6-astra, gpt-6-luna, gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna]
', 0, 0
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE name = 'codex-subscription');

INSERT OR IGNORE INTO models (provider, name)
VALUES ('codex-subscription', 'gpt-6.1-sol'), ('codex-subscription', 'gpt-6-sol'),
       ('codex-subscription', 'gpt-6-astra'), ('codex-subscription', 'gpt-6-luna'),
       ('codex-subscription', 'gpt-5.6-sol'), ('codex-subscription', 'gpt-5.6-terra'),
       ('codex-subscription', 'gpt-5.6-luna');

-- +goose Down
DELETE FROM models WHERE provider = 'codex-subscription';
DELETE FROM providers WHERE name = 'codex-subscription';
