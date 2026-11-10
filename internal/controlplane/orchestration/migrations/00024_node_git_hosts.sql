-- +goose Up
-- The git hosts a node reached when it last tried, as a JSON array of host names. NULL is a node that has not
-- said: one from before nodes tried, which is taken to reach every host.
ALTER TABLE nodes ADD COLUMN git_hosts TEXT;

-- +goose Down
ALTER TABLE nodes DROP COLUMN git_hosts;
