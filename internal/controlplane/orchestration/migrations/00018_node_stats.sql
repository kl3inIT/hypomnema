-- What a node measures (issue #69, step 2): the latest values only, sent with its heartbeat (Coder and Nezha keep
-- only the latest by default; history is a later step).

-- +goose Up
ALTER TABLE nodes ADD COLUMN cpu_percent REAL;
ALTER TABLE nodes ADD COLUMN memory_used_bytes INTEGER;
-- Of the volume that holds the worktree root.
ALTER TABLE nodes ADD COLUMN disk_used_bytes INTEGER;
ALTER TABLE nodes ADD COLUMN load1 REAL;
ALTER TABLE nodes ADD COLUMN stats_at INTEGER;

-- +goose Down
ALTER TABLE nodes DROP COLUMN stats_at;
ALTER TABLE nodes DROP COLUMN load1;
ALTER TABLE nodes DROP COLUMN disk_used_bytes;
ALTER TABLE nodes DROP COLUMN memory_used_bytes;
ALTER TABLE nodes DROP COLUMN cpu_percent;
