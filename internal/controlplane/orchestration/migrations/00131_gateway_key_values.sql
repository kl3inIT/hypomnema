-- +goose Up
ALTER TABLE gateway_keys ADD COLUMN secret_version_id TEXT REFERENCES secret_versions(id);
ALTER TABLE gateway_keys ADD COLUMN masked_value TEXT NOT NULL DEFAULT '';

-- +goose Down
CREATE TEMP TABLE gateway_key_secrets AS
SELECT v.secret_id FROM gateway_keys k JOIN secret_versions v ON k.secret_version_id = v.id;
ALTER TABLE gateway_keys DROP COLUMN masked_value;
ALTER TABLE gateway_keys DROP COLUMN secret_version_id;
DELETE FROM secrets WHERE id IN (SELECT secret_id FROM gateway_key_secrets);
DROP TABLE gateway_key_secrets;
