-- The vendor's project an account logged in on the control plane works in (issue #20, step 4): for an Antigravity
-- account, the Cloud Code project every request names. Found at login; not a secret.

-- +goose Up
ALTER TABLE accounts ADD COLUMN project TEXT;

-- +goose Down
ALTER TABLE accounts DROP COLUMN project;
