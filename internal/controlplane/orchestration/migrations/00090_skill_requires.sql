-- What a skill's revision requires of an agent to be given to it (issue #536), read from its SKILL.md's
-- metadata.hypomnema.requires as its description is from its frontmatter, after OpenClaw's requires gating
-- (docs/tools/skills.md) and OpenMausBot's requiredCapabilities (server/skill-library.ts): requirements
-- "kind:name" joined by commas ("integration:superset", "device:android"), NULL for none. A revision kept before is
-- given as it was.

-- +goose Up
ALTER TABLE skill_revisions ADD COLUMN requires TEXT;

-- +goose Down
ALTER TABLE skill_revisions DROP COLUMN requires;
