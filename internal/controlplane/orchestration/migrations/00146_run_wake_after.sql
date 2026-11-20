-- +goose Up
ALTER TABLE runs ADD COLUMN wake_after INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE runs DROP COLUMN wake_after;
