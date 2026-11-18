-- +goose Up
ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'broad' CHECK (kind IN ('broad', 'narrow', 'quick'));
ALTER TABLE tasks ADD COLUMN author_vendor TEXT NOT NULL DEFAULT '';
ALTER TABLE dispatches ADD COLUMN role_chain TEXT NOT NULL DEFAULT '';
ALTER TABLE dispatches ADD COLUMN chain_entry INTEGER;

CREATE TABLE role_routing (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    source TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE role_routing;
ALTER TABLE dispatches DROP COLUMN chain_entry;
ALTER TABLE dispatches DROP COLUMN role_chain;
ALTER TABLE tasks DROP COLUMN author_vendor;
ALTER TABLE tasks DROP COLUMN kind;
