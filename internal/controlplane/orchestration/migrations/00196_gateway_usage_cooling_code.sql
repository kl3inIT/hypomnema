-- Why a request moved on to the next target or to the Bot's fallback (issue #1335, row 6): the cooling code of the
-- refusal it moved on for (usage_limit, rate_limit, billing, auth, overloaded), the set account_cooldowns.code keeps
-- (00190); empty when it did not move on. OmniRoute keeps lastErrorType beside a connection's last error
-- (src/lib/db/providers.ts:756-876).

-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN cooling_code TEXT NOT NULL DEFAULT ''
    CHECK (cooling_code IN ('', 'usage_limit', 'rate_limit', 'billing', 'auth', 'overloaded', 'unknown'));

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN cooling_code;
