-- A profile's backup profiles, and the Dispatch that ran under one (issue #171). The owner orders the backups per
-- profile, as OpenMausBot's list per bot (server/host/limit-takeover.ts) and OpenClaw's fallbacks per agent
-- (docs/concepts/model-failover.md); a Task that has not started moves to the first one a worker can start under
-- when every account of its own profile's runtime waits for a limit to reset.

-- +goose Up
-- Profile names in order, separated by commas; empty for none.
ALTER TABLE profile_versions ADD COLUMN fallbacks TEXT NOT NULL DEFAULT '';
-- The profile the Task asked for, when the Dispatch ran under a backup of it, and when that profile's accounts
-- were to be free again: one stretch of a limit, told once.
ALTER TABLE dispatches ADD COLUMN fallback_for TEXT;
ALTER TABLE dispatches ADD COLUMN fallback_until INTEGER;

-- +goose Down
ALTER TABLE dispatches DROP COLUMN fallback_until;
ALTER TABLE dispatches DROP COLUMN fallback_for;
ALTER TABLE profile_versions DROP COLUMN fallbacks;
