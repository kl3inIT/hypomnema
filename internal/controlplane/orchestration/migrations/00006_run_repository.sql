-- The repository a Run works in (issue #32): its workers make worktrees of `repo` (owner/name) from `base`.

-- +goose Up
ALTER TABLE runs ADD COLUMN repo TEXT;
ALTER TABLE runs ADD COLUMN base TEXT;

-- +goose Down
ALTER TABLE runs DROP COLUMN base;
ALTER TABLE runs DROP COLUMN repo;
