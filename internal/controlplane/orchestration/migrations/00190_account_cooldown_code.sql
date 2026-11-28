-- Why an account rests, in one closed set beside the provider's own words in reason (issue #1305, row 5): usage_limit
-- (a plan's window or allowance used up), rate_limit (a short throttle), billing (credits or a balance spent), auth
-- (the key or sign-in refused), overloaded (the provider out of capacity), unknown. OmniRoute keeps errorCode beside
-- lastError on a connection (src/lib/db/providers.ts:756-876), Bifrost failure_class beside the error
-- (core/schemas/bifrost.go:782). A cooldown a node's Dispatch set after a rate limit (reason rate_limited) is a
-- rate limit; one the gateway set before this migration says nothing more than its words.

-- +goose Up
ALTER TABLE account_cooldowns ADD COLUMN code TEXT NOT NULL DEFAULT 'unknown'
    CHECK (code IN ('usage_limit', 'rate_limit', 'billing', 'auth', 'overloaded', 'unknown'));

UPDATE account_cooldowns SET code = 'rate_limit' WHERE reason = 'rate_limited';

-- +goose Down
ALTER TABLE account_cooldowns DROP COLUMN code;
