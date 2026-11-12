-- name: InsertSkill :exec
INSERT INTO skills (name, created_at, updated_at) VALUES (@name, @now, @now) ON CONFLICT (name) DO NOTHING;

-- name: TouchSkill :exec
UPDATE skills SET updated_at = @now WHERE name = @name;

-- name: SetSkillEveryone :execrows
UPDATE skills SET everyone = @everyone, updated_at = @now WHERE name = @name;

-- name: DeleteSkill :execrows
DELETE FROM skills WHERE name = @name;

-- name: InsertSkillRevision :exec
INSERT INTO skill_revisions (skill, revision, description, digest, size, files, source, source_commit, archive,
    created_at)
VALUES (@skill, @revision, @description, @digest, @size, @files, @source, @source_commit, @archive, @now);

-- name: ListSkills :many
SELECT s.name, s.everyone, s.created_at, s.updated_at,
    r.revision, r.description, r.digest, r.size, r.files, r.source, r.source_commit, r.created_at AS revised_at
FROM skills s JOIN skill_revisions r ON r.skill = s.name
WHERE r.revision = (SELECT max(revision) FROM skill_revisions WHERE skill = s.name)
ORDER BY s.name;

-- name: GetSkill :one
SELECT * FROM skills WHERE name = @name;

-- name: ListSkillRevisions :many
SELECT skill, revision, description, digest, size, files, source, source_commit, created_at
FROM skill_revisions WHERE skill = @skill ORDER BY revision DESC;

-- name: LatestSkillRevision :one
SELECT revision, digest FROM skill_revisions WHERE skill = @skill ORDER BY revision DESC LIMIT 1;
