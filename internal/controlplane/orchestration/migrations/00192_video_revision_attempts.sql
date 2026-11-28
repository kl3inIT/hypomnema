-- +goose Up
ALTER TABLE video_revisions ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE video_revisions DROP COLUMN attempts;
