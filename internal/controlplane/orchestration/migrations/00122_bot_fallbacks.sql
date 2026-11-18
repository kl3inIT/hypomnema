-- +goose Up
ALTER TABLE bots ADD COLUMN fallbacks TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE bots DROP COLUMN fallbacks;
