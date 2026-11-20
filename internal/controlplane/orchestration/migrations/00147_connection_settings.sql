-- What the owner set for a Connection beside its credential (issue #976), as JSON the Integration reads: for a
-- BigQuery Connection, the billing project, the GA4 export datasets, the location and the cap on the bytes one
-- query may bill. Not a secret: the key is in the vault. NULL for the Connections that have no settings.

-- +goose Up
ALTER TABLE connections ADD COLUMN settings TEXT;

-- +goose Down
ALTER TABLE connections DROP COLUMN settings;
