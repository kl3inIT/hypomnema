-- A provider's machine can be stopped and started (issue #69): stopped, it keeps its disk and costs no compute,
-- as Coder's AWS template stops an instance instead of destroying it. NULL is a machine that is on.

-- +goose Up
-- stopping: asked to stop; the provider is asked until it has. stopped: off. starting: asked to start, until the
-- node says hello.
ALTER TABLE nodes ADD COLUMN machine_power TEXT CHECK (machine_power IN ('stopping', 'stopped', 'starting'));

-- +goose Down
ALTER TABLE nodes DROP COLUMN machine_power;
