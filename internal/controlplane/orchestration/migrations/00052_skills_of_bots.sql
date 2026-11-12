-- A Bot's skills (issue #21, step 3): the skills it adds beside those given to everyone and the ones for everyone it
-- turns off, as a profile's (migration 00051), after OpenMausBot's per-bot skills and OpenClaw's per-agent list.

-- +goose Up
ALTER TABLE bots ADD COLUMN skills TEXT NOT NULL DEFAULT '';
ALTER TABLE bots ADD COLUMN skills_off TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE bots DROP COLUMN skills_off;
ALTER TABLE bots DROP COLUMN skills;
