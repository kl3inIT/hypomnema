-- The full Claude family the subscription serves at the gateway (issue #20, step 5). The claude-subscription
-- provider was seeded (00008) with the 5.5 models and their short aliases; this adds Fable, the top model, and its
-- alias, so every Claude model the owner's plan serves is on the gateway (owner, 2026-11-11).

-- +goose Up
INSERT OR IGNORE INTO models (provider, name)
VALUES ('claude-subscription', 'claude-fable-5-2'), ('claude-subscription', 'fable');

-- +goose Down
DELETE FROM models WHERE provider = 'claude-subscription' AND name IN ('claude-fable-5-2', 'fable');
