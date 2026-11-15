-- A user Connection of an app Integration (issue #468): the owner signed in to Lark as himself through one of his
-- apps, as lark-cli's user login uses an app's id and secret (internal/auth/device_flow.go). app_label names the
-- app Connection of the same Integration whose app it signs in with; app_id is that app's id when he signed in,
-- so a Connection whose app was changed since is known to need a sign-in again. NULL for every other Connection.

-- +goose Up
ALTER TABLE connections ADD COLUMN app_label TEXT;

-- +goose Down
DELETE FROM connections WHERE app_label IS NOT NULL;
ALTER TABLE connections DROP COLUMN app_label;
