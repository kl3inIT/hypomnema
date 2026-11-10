-- A description for each profile (issue #49), as omp's agent definitions and Omnara's subagents have: what the
-- profile is for, so a coordinator chooses by it. Versions applied before it have none.

-- +goose Up
ALTER TABLE profile_versions ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- The starting set gets a second version with its description, where the owner has not changed the profile.
INSERT INTO profile_versions (id, profile, version, source, digest, runtime, instruction, description, created_at)
SELECT 'pfv_seed2_implementer', 'implementer', 2, 'name: implementer
description: Makes a change in the repository, runs its checks and opens a pull request. The default for any change.
runtime: claude
', '51efe1c01558e146768c504ec245e9ece06c6264f75c5df5cb90445a55bcf7c3', 'claude', NULL,
       'Makes a change in the repository, runs its checks and opens a pull request. The default for any change.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'implementer' AND current_version_id = 'pfv_seed_implementer');
UPDATE profiles SET current_version_id = 'pfv_seed2_implementer'
WHERE name = 'implementer' AND current_version_id = 'pfv_seed_implementer';

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, instruction, description, created_at)
SELECT 'pfv_seed2_reviewer', 'reviewer', 2, 'name: reviewer
description: Reviews a change against its spec and reports problems with file and line; changes files only when the task asks for fixes.
runtime: claude
instruction: |
  Review the change the task names. Report what is wrong or missing, with file and line, as the summary of
  `finish`; change files only when the task asks for fixes.
', 'e97d958bbf518899fe985f44140a890c7857fe48fa11436f1ec3eadf889e8e84', 'claude', 'Review the change the task names. Report what is wrong or missing, with file and line, as the summary of
`finish`; change files only when the task asks for fixes.
',
       'Reviews a change against its spec and reports problems with file and line; changes files only when the task asks for fixes.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'reviewer' AND current_version_id = 'pfv_seed_reviewer');
UPDATE profiles SET current_version_id = 'pfv_seed2_reviewer'
WHERE name = 'reviewer' AND current_version_id = 'pfv_seed_reviewer';

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, instruction, description, created_at)
SELECT 'pfv_seed2_explorer', 'explorer', 2, 'name: explorer
description: Investigates code or a question and reports what it found, with file paths; changes nothing.
runtime: claude
instruction: |
  Read and report only: do not change, commit or push files, and open no pull request. Put what you found, with
  file paths, in the summary of `finish`.
', '8cf6fa11fd54a51c2a4eb5e6ffbd2f454c1a8f62016a0a9947774fa0fc952646', 'claude', 'Read and report only: do not change, commit or push files, and open no pull request. Put what you found, with
file paths, in the summary of `finish`.
',
       'Investigates code or a question and reports what it found, with file paths; changes nothing.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'explorer' AND current_version_id = 'pfv_seed_explorer');
UPDATE profiles SET current_version_id = 'pfv_seed2_explorer'
WHERE name = 'explorer' AND current_version_id = 'pfv_seed_explorer';

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, instruction, description, created_at)
SELECT 'pfv_seed2_coordinator', 'coordinator', 2, 'name: coordinator
description: Plans a Run, gives its Tasks to workers and reports their outcomes; pi on the server.
runtime: pi
', '4950f549214f81771d5b75602230e57d3083beeaa51b918f504a046be25ccebe', 'pi', NULL,
       'Plans a Run, gives its Tasks to workers and reports their outcomes; pi on the server.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'coordinator' AND current_version_id = 'pfv_seed_coordinator');
UPDATE profiles SET current_version_id = 'pfv_seed2_coordinator'
WHERE name = 'coordinator' AND current_version_id = 'pfv_seed_coordinator';

-- +goose Down
UPDATE profiles SET current_version_id = 'pfv_seed_implementer' WHERE current_version_id = 'pfv_seed2_implementer';
UPDATE profiles SET current_version_id = 'pfv_seed_reviewer' WHERE current_version_id = 'pfv_seed2_reviewer';
UPDATE profiles SET current_version_id = 'pfv_seed_explorer' WHERE current_version_id = 'pfv_seed2_explorer';
UPDATE profiles SET current_version_id = 'pfv_seed_coordinator' WHERE current_version_id = 'pfv_seed2_coordinator';
DELETE FROM profile_versions WHERE id LIKE 'pfv_seed2_%';
ALTER TABLE profile_versions DROP COLUMN description;
