-- What each action of a Connection's Integration the account can really do (issue #567): granted, missing or
-- unknown, with a short reason, read at the service when the Connection is made or signed in again and when the
-- owner checks it again. JSON, as the service proxy's catalog has the actions and this store none. NULL until it
-- was read once.

-- +goose Up
ALTER TABLE connections ADD COLUMN access TEXT;

-- +goose Down
ALTER TABLE connections DROP COLUMN access;
