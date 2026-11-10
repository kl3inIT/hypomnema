-- Refreshing a subscription's tokens (issue #66): one refresher per secret, and a mark for a login the provider no
-- longer accepts. After Omnara's secret_oauth_refresh_leases (migrations/000001_identity.sql). Only
-- internal/controlplane/vault reads and writes these.

-- +goose Up
-- Who is refreshing a token set now. The holder may store the result only while the secret is still at the version
-- it read, so a refresher that stalled cannot overwrite a newer one.
CREATE TABLE secret_refresh_leases (
    secret_id           TEXT    NOT NULL PRIMARY KEY REFERENCES secrets (id) ON DELETE CASCADE,
    owner_token         TEXT    NOT NULL CHECK (length(owner_token) > 0),
    expected_version_id TEXT    NOT NULL,
    expires_at          INTEGER NOT NULL
) STRICT;

-- When the provider refused the refresh token for good; NULL while the login is good. A new login (a rotation)
-- clears it.
ALTER TABLE secrets ADD COLUMN login_required_at INTEGER;

-- +goose Down
ALTER TABLE secrets DROP COLUMN login_required_at;
DROP TABLE secret_refresh_leases;
