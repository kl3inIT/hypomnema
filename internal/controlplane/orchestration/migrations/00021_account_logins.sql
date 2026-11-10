-- Subscription accounts the owner logged in through the control plane (issue #20, step 2). Such an account's
-- tokens are a secret of the vault, which the account names; who it is at the vendor is kept here, with no
-- secret value. An account added by hand has none of this: its CLI was logged in on a node.

-- +goose Up

-- The vendor's own id of the account (Codex: the ChatGPT account id). A new login of the same one replaces its tokens.
ALTER TABLE accounts ADD COLUMN external_id TEXT;
ALTER TABLE accounts ADD COLUMN email TEXT;
ALTER TABLE accounts ADD COLUMN plan TEXT;
-- Where the tokens are kept, as a provider's api_key_ref: vault:<name>.
ALTER TABLE accounts ADD COLUMN secret_ref TEXT;
-- Refreshes that failed in a row for a reason that may pass, when the next one may be tried, and why the last
-- one failed. A refresh that worked clears all three.
ALTER TABLE accounts ADD COLUMN refresh_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN refresh_retry_at INTEGER;
ALTER TABLE accounts ADD COLUMN refresh_error TEXT;

CREATE UNIQUE INDEX accounts_external ON accounts (runtime, external_id) WHERE external_id IS NOT NULL;

-- +goose Down
DROP INDEX accounts_external;
ALTER TABLE accounts DROP COLUMN refresh_error;
ALTER TABLE accounts DROP COLUMN refresh_retry_at;
ALTER TABLE accounts DROP COLUMN refresh_failures;
ALTER TABLE accounts DROP COLUMN secret_ref;
ALTER TABLE accounts DROP COLUMN plan;
ALTER TABLE accounts DROP COLUMN email;
ALTER TABLE accounts DROP COLUMN external_id;
