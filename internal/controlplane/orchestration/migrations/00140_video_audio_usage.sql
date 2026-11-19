-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN video_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_usage ADD COLUMN video_rev INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE gateway_usage DROP COLUMN video_rev;
ALTER TABLE gateway_usage DROP COLUMN video_id;
