-- A gateway key's own routing (issue #1277), stored next to the key as gateway_search_providers keeps the global
-- order. own_accounts says the key may use only the accounts in gateway_key_accounts (OmniRoute's allowed_connections,
-- Bifrost's key_ids): 0 is every account, as before. It is kept apart from the rows so that a key whose chosen
-- accounts were all removed uses none, not every one. gateway_key_search_order is the key's own web search order,
-- the providers it leaves out following in the global order; no row is the global order.

-- +goose Up
ALTER TABLE gateway_keys ADD COLUMN own_accounts INTEGER NOT NULL DEFAULT 0 CHECK (own_accounts IN (0, 1));

CREATE TABLE gateway_key_accounts (
    key_id     TEXT NOT NULL REFERENCES gateway_keys (id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, account_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX gateway_key_accounts_account ON gateway_key_accounts (account_id);

CREATE TABLE gateway_key_search_order (
    key_id   TEXT    NOT NULL REFERENCES gateway_keys (id) ON DELETE CASCADE,
    provider TEXT    NOT NULL CHECK (length(provider) > 0),
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (key_id, provider),
    UNIQUE (key_id, position)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE gateway_key_search_order;
DROP TABLE gateway_key_accounts;
ALTER TABLE gateway_keys DROP COLUMN own_accounts;
