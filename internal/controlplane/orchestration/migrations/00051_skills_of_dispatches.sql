-- The skills a worker gets (issue #21, step 2): a profile version adds skills and turns off skills given to
-- everyone, as OpenMausBot's bot opts out of an everyBot skill and OpenClaw's agent lists its own; a Dispatch
-- records the revision of each skill it was given when it was made, as an OpenClaw session keeps the revisions it
-- selected. The record stays when a skill is removed, so a Dispatch still says what it had.

-- +goose Up
ALTER TABLE profile_versions ADD COLUMN skills TEXT NOT NULL DEFAULT '';
ALTER TABLE profile_versions ADD COLUMN skills_off TEXT NOT NULL DEFAULT '';

CREATE TABLE dispatch_skills (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    skill       TEXT    NOT NULL,
    revision    INTEGER NOT NULL,
    digest      TEXT    NOT NULL,
    PRIMARY KEY (dispatch_id, skill)
) STRICT;

CREATE INDEX dispatch_skills_digest ON dispatch_skills (digest);

-- +goose Down
DROP TABLE dispatch_skills;
ALTER TABLE profile_versions DROP COLUMN skills_off;
ALTER TABLE profile_versions DROP COLUMN skills;
