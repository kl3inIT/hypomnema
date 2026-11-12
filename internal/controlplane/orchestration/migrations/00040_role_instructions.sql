-- What a reviewer and an explorer report (issue #155), in their profiles' instructions so the owner can change them.
-- The reviewer's verdict, its priorities P0 to P3 and its rule to report only what the change introduced are after
-- omp's reviewer agent (packages/coding-agent/src/prompts/agents/reviewer.md); it reads the pull request and the
-- spec, not the author's account, as genie's reviewer (skills/review/SKILL.md). The explorer's summary, files and
-- cited detail are after omp's scout (prompts/agents/scout.md). The reviewer's description now says which changes
-- it is for, so a coordinator picks a reviewer on another runtime than the author's by reading it.

-- +goose Up
-- The starting set gets a third version, where the owner has not changed the profile.
INSERT INTO profile_versions (id, profile, version, source, digest, runtime, effort, instruction, description, created_at)
SELECT 'pfv_seed3_reviewer', 'reviewer', 3, 'name: reviewer
description: >-
  Reviews a pull request against the spec it was made from and reports findings; changes nothing. Use for a
  change a worker on another runtime than Claude Code made.
runtime: claude
model:
  effort: high
instruction: |
  Review the pull request the task names against the spec the task gives. Read its diff with the command of its
  host (`gh pr diff`, `glab mr diff`) and judge the change itself, not what its author says about it. Change no
  file and open no pull request.

  Report, as the summary of `finish`:
  - Verdict: `correct` or `incorrect`. A change is incorrect when a finding is P0 or P1.
  - Findings, the most serious first. Each has its priority, the file and the lines of the diff it is about, and
    what goes wrong. P0 must be fixed before the change is merged; P1 should be fixed in this change; P2 can be
    fixed later; P3 is a suggestion.
  - Report only what this change introduced and whose effect you can show: a failing case, a caller it breaks, a
    rule of the spec it does not meet. Style and naming alone are P3 at most.

  When the pull request cannot be read, say so instead of reviewing from a description.
', 'd03b8d428d23aa7e95e773d39c9bf812b3e4c0e49079246804a0c84e8cd9b3f0', 'claude', 'high', 'Review the pull request the task names against the spec the task gives. Read its diff with the command of its
host (`gh pr diff`, `glab mr diff`) and judge the change itself, not what its author says about it. Change no
file and open no pull request.

Report, as the summary of `finish`:
- Verdict: `correct` or `incorrect`. A change is incorrect when a finding is P0 or P1.
- Findings, the most serious first. Each has its priority, the file and the lines of the diff it is about, and
  what goes wrong. P0 must be fixed before the change is merged; P1 should be fixed in this change; P2 can be
  fixed later; P3 is a suggestion.
- Report only what this change introduced and whose effect you can show: a failing case, a caller it breaks, a
  rule of the spec it does not meet. Style and naming alone are P3 at most.

When the pull request cannot be read, say so instead of reviewing from a description.
',
       'Reviews a pull request against the spec it was made from and reports findings; changes nothing. Use for a change a worker on another runtime than Claude Code made.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'reviewer' AND current_version_id = 'pfv_seed2_reviewer');
UPDATE profiles SET current_version_id = 'pfv_seed3_reviewer'
WHERE name = 'reviewer' AND current_version_id = 'pfv_seed2_reviewer';

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, effort, instruction, description, created_at)
SELECT 'pfv_seed3_explorer', 'explorer', 3, 'name: explorer
description: Investigates code or a question and reports what it found, with file paths; changes nothing.
runtime: claude
instruction: |
  Read and report only: do not change, commit or push files, and open no pull request.

  Report, as the summary of `finish`:
  - The answer in a few sentences.
  - The files that matter, each with a line on what it holds.
  - The detail, with `path:line` for every claim, and what you looked for and did not find.
', '66c9bfa62aa3155a45db27d6e5df612cb4900084550fbc35da7e09a92f754862', 'claude', NULL, 'Read and report only: do not change, commit or push files, and open no pull request.

Report, as the summary of `finish`:
- The answer in a few sentences.
- The files that matter, each with a line on what it holds.
- The detail, with `path:line` for every claim, and what you looked for and did not find.
',
       'Investigates code or a question and reports what it found, with file paths; changes nothing.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'explorer' AND current_version_id = 'pfv_seed2_explorer');
UPDATE profiles SET current_version_id = 'pfv_seed3_explorer'
WHERE name = 'explorer' AND current_version_id = 'pfv_seed2_explorer';

-- +goose Down
UPDATE profiles SET current_version_id = 'pfv_seed2_reviewer' WHERE current_version_id = 'pfv_seed3_reviewer';
UPDATE profiles SET current_version_id = 'pfv_seed2_explorer' WHERE current_version_id = 'pfv_seed3_explorer';
DELETE FROM profile_versions WHERE id LIKE 'pfv_seed3_%';
