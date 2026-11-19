-- An Antigravity account Google wants verified (issue #822): a 403 with reason VALIDATION_REQUIRED on chat, on the
-- models list or on the quota read marks the account, as opencode-antigravity-auth keeps verificationRequired,
-- verificationRequiredAt, verificationRequiredReason and verificationUrl on the account (src/plugin/storage.ts:202-205)
-- and its PR #338 a count of the failures for its 10 min / 1 h / 6 h / 24 h schedule. Read by the gateway, which
-- skips the account until verify_retry_at; cleared by a successful call under the active sign-in or by signing in again.

-- +goose Up
-- The link Google gave last, its message, and when it first asked (ms since 1970); NULL when not marked.
ALTER TABLE accounts ADD COLUMN verify_url TEXT;
ALTER TABLE accounts ADD COLUMN verify_message TEXT;
ALTER TABLE accounts ADD COLUMN verify_asked_at INTEGER;
-- The initial refusal plus retry slots claimed, and when the account is tried again.
ALTER TABLE accounts ADD COLUMN verify_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN verify_retry_at INTEGER;
ALTER TABLE accounts ADD COLUMN verify_login_version INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE accounts DROP COLUMN verify_login_version;
ALTER TABLE accounts DROP COLUMN verify_retry_at;
ALTER TABLE accounts DROP COLUMN verify_failures;
ALTER TABLE accounts DROP COLUMN verify_asked_at;
ALTER TABLE accounts DROP COLUMN verify_message;
ALTER TABLE accounts DROP COLUMN verify_url;
