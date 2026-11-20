-- Whether a node runs its workers with their Dispatch's temp directory as /tmp and /var/tmp (issue #895), as it says
-- at each hello, and why not when it does not. NULL: a node of an earlier release, which does not say.

-- +goose Up
ALTER TABLE nodes ADD COLUMN temp_isolation INTEGER CHECK (temp_isolation IN (0, 1));
ALTER TABLE nodes ADD COLUMN temp_isolation_reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN temp_isolation_reason;
ALTER TABLE nodes DROP COLUMN temp_isolation;
