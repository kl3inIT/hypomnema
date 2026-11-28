-- One memory ceiling for a node's workers (issue #1313). Whether a node runs its workers in its systemd slice under
-- the ceiling, as it says at each hello, and why not when it does not; NULL: a node of an earlier release, which does
-- not say. And a profile's own limit for each of its workers, from its YAML's limits.memory, in bytes; NULL: none, as
-- a Kubernetes container's limits are optional.

-- +goose Up
ALTER TABLE nodes ADD COLUMN memory_limit INTEGER CHECK (memory_limit IN (0, 1));
ALTER TABLE nodes ADD COLUMN memory_limit_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE profile_versions ADD COLUMN memory_limit_bytes INTEGER CHECK (memory_limit_bytes > 0);

-- +goose Down
ALTER TABLE profile_versions DROP COLUMN memory_limit_bytes;
ALTER TABLE nodes DROP COLUMN memory_limit_reason;
ALTER TABLE nodes DROP COLUMN memory_limit;
