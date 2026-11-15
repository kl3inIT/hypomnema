-- The tools an agent gets (issue #536, step 7; #492, row 10): a profile version and a Bot narrow the control plane's
-- tools with two lists beside their skills (migrations 00051, 00052), the tools they keep (empty: every one their
-- kind gets) and the tools they turn off, each a name or a family (`agent_browser_*`), as OpenClaw's agent has
-- tools.allow and tools.deny and OpenMausBot's bot an Allow and an Exclude list.

-- +goose Up
ALTER TABLE profile_versions ADD COLUMN tools TEXT NOT NULL DEFAULT '';
ALTER TABLE profile_versions ADD COLUMN tools_off TEXT NOT NULL DEFAULT '';
ALTER TABLE bots ADD COLUMN tools TEXT NOT NULL DEFAULT '';
ALTER TABLE bots ADD COLUMN tools_off TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE bots DROP COLUMN tools_off;
ALTER TABLE bots DROP COLUMN tools;
ALTER TABLE profile_versions DROP COLUMN tools_off;
ALTER TABLE profile_versions DROP COLUMN tools;
