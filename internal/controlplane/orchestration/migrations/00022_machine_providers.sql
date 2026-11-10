-- Machines created at a provider (issue #69, step 3). A machine provider names the vault secret that holds its
-- key (Omnara seals a provider's key as a secret of the organisation); a node made by one records which, the
-- provider's id for the machine, and where the machine is between "asked for" and "gone" (Omnara's source_kind,
-- provider, provider_resource_id and lifecycle state). A node the owner installed has none of these.

-- +goose Up
CREATE TABLE machine_providers (
    id           TEXT    NOT NULL PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    kind         TEXT    NOT NULL CHECK (kind IN ('aws')),
    -- Where the provider's key is: a reference to a vault secret.
    key_ref      TEXT    NOT NULL CHECK (length(key_ref) > 0),
    -- The region and size a machine gets when the owner names none.
    region       TEXT    NOT NULL CHECK (length(region) > 0),
    size         TEXT    NOT NULL CHECK (length(size) > 0),
    -- How many machines of this provider may exist at once.
    max_machines INTEGER NOT NULL CHECK (max_machines > 0),
    -- What only this kind of provider takes, as a JSON object of strings.
    options      TEXT    NOT NULL DEFAULT '{}',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

-- Not a foreign key: a removed node keeps the id of a provider that may be removed after it.
ALTER TABLE nodes ADD COLUMN provider_id TEXT;
-- The provider's id for the machine, once the provider has answered.
ALTER TABLE nodes ADD COLUMN provider_resource_id TEXT;
ALTER TABLE nodes ADD COLUMN region TEXT;
ALTER TABLE nodes ADD COLUMN size TEXT;
-- provisioning: asked for, not heard from yet. ready: it said hello. provision_failed: the provider refused or
-- the machine never said hello. deleting, delete_failed: being destroyed at the provider; a failure is retried.
ALTER TABLE nodes ADD COLUMN machine_state TEXT
    CHECK (machine_state IN ('provisioning', 'ready', 'provision_failed', 'deleting', 'delete_failed'));
-- Why the last step failed, for the owner.
ALTER TABLE nodes ADD COLUMN machine_error TEXT;
-- Failed deletions so far, and when the next one is due.
ALTER TABLE nodes ADD COLUMN machine_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN machine_retry_at INTEGER;

CREATE INDEX nodes_provider ON nodes (provider_id) WHERE provider_id IS NOT NULL;

-- +goose Down
DROP INDEX nodes_provider;
ALTER TABLE nodes DROP COLUMN machine_retry_at;
ALTER TABLE nodes DROP COLUMN machine_attempts;
ALTER TABLE nodes DROP COLUMN machine_error;
ALTER TABLE nodes DROP COLUMN machine_state;
ALTER TABLE nodes DROP COLUMN size;
ALTER TABLE nodes DROP COLUMN region;
ALTER TABLE nodes DROP COLUMN provider_resource_id;
ALTER TABLE nodes DROP COLUMN provider_id;
DROP TABLE machine_providers;
