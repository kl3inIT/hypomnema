-- A node's GPUs in its latest measurement (issue #69), as Beszel's GPUData: a JSON list by index, NULL for a
-- machine that has none or whose node reads none. Only the latest is kept, as the rest of a node's stats.

-- +goose Up
ALTER TABLE nodes ADD COLUMN gpus TEXT;

-- +goose Down
ALTER TABLE nodes DROP COLUMN gpus;
