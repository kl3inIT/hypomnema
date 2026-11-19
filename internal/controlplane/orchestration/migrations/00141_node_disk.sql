-- +goose Up
ALTER TABLE nodes ADD COLUMN disk_policy INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN disk_free_bytes INTEGER;
ALTER TABLE nodes ADD COLUMN disk_floor_bytes INTEGER;
ALTER TABLE nodes ADD COLUMN disk_trim_bytes INTEGER;
ALTER TABLE nodes ADD COLUMN disk_sample_at INTEGER;
ALTER TABLE dispatches ADD COLUMN declined_low_disk INTEGER NOT NULL DEFAULT 0;
CREATE INDEX conversations_node_worktree ON conversations (node_id, worktree);

-- +goose Down
DROP INDEX conversations_node_worktree;
ALTER TABLE dispatches DROP COLUMN declined_low_disk;
ALTER TABLE nodes DROP COLUMN disk_sample_at;
ALTER TABLE nodes DROP COLUMN disk_trim_bytes;
ALTER TABLE nodes DROP COLUMN disk_floor_bytes;
ALTER TABLE nodes DROP COLUMN disk_free_bytes;
ALTER TABLE nodes DROP COLUMN disk_policy;
