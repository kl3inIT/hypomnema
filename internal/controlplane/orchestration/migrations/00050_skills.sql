-- Skills (issue #21): a skill in the Agent Skills format that the owner adds, kept whole in revisions that never
-- change, after Omnara's skills and skill_revisions (migrations/000009_skills.sql). The archive is kept in the
-- database, as Coder keeps a template's archive in its files table by hash: the control plane has no other place
-- for files, and one database is one backup.

-- +goose Up
CREATE TABLE skills (
    name       TEXT    NOT NULL PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
    -- Given to every worker and Bot that does not turn it off (OpenMausBot's everyBot); off when added.
    everyone   INTEGER NOT NULL DEFAULT 0 CHECK (everyone IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE skill_revisions (
    skill         TEXT    NOT NULL REFERENCES skills (name) ON DELETE CASCADE,
    revision      INTEGER NOT NULL CHECK (revision > 0),
    description   TEXT    NOT NULL,
    -- SHA-256 of the files (internal/shared/skillpack): the same folder added again makes no new revision.
    digest        TEXT    NOT NULL CHECK (length(digest) = 64),
    size          INTEGER NOT NULL,
    files         INTEGER NOT NULL,
    -- Where it came from: "upload", or "github:owner/repo[/path]@ref" with the commit the ref was.
    source        TEXT    NOT NULL,
    source_commit TEXT,
    -- The files as a gzipped tar that does not depend on how they were given.
    archive       BLOB    NOT NULL,
    created_at    INTEGER NOT NULL,
    PRIMARY KEY (skill, revision)
) STRICT;

-- +goose Down
DROP TABLE skill_revisions;
DROP TABLE skills;
