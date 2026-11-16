-- The starting profiles' instructions as issue #672 rewrote them, after the references and the vendors' guides
-- (study of 2026-11-17 in the issue). As 00040 and 00081 do, a new version is made only where the profile's current
-- version is still the seed's latest (pfv_seed3_implementer, pfv_seed4_reviewer, pfv_seed4_explorer) and runs, as
-- 00090 left it on a fresh install, on the Claude subscription; a profile the owner applied keeps its own, and the
-- owner applies the new text there (`hypomnema profiles apply`). Each keeps its seed's description, runtime, model
-- and effort. Source and digest are what ApplyProfile writes for that YAML, so applying the same YAML later makes no
-- new version.
--
-- - implementer, which had no instruction: what done means. OpenAI's GPT-6 guide, "Define what 'done'
--   includes—implementing the change, running it, inspecting the result, and fixing failures"; Omnigent's Polly
--   codex agent, "drive it to green: run the relevant tests, lint, and typecheck for the code you touched", "include
--   the exact command", and a pull request with "what changed, how you verified" (examples/polly/agents/codex/
--   config.yaml:20-32). In this instruction, not the worker's preamble, since the explorer and the reviewer get the
--   preamble too and change nothing.
-- - reviewer: Codex's review rubric (codex-rs/prompts/templates/review/rubric.md), the review prompt of the vendor
--   whose model runs the owner's reviewer: every finding, not the first only (:36); the conditions under which each
--   one goes wrong, and P0 only for what breaks whatever the inputs (:27, :56); the repository rule a finding rests
--   on (:46-52); `correct` means existing code and tests will not break (:61). Reading each changed file in full and
--   the code that consumes what the change emits, outside the diff too: omp's reviewer
--   (packages/coding-agent/src/prompts/agents/reviewer.md:61,78-85) and gemini-cli's investigator
--   (packages/core/src/agents/codebase-investigator.ts:137). P1 stays "should be fixed in this change", not Codex's
--   and omp's "next cycle": here the verdict gates the merge.
-- - explorer: omp's scout (packages/coding-agent/src/prompts/agents/scout.md): another search before saying something
--   does not exist (:47), how the pieces connect (:41), no command that changes state (:65); gemini-cli's
--   investigator, not to stop at the first relevant file and to check callers (codebase-investigator.ts:137-140).

-- +goose Up
INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              description, created_at)
SELECT 'pfv_seed4_implementer', 'implementer',
       (SELECT max(version) + 1 FROM profile_versions WHERE profile = 'implementer'), 'name: implementer
description: Makes a change in the repository, runs its checks and opens a pull request. The default for any change.
runtime: claude
model:
  provider: claude-subscription
  name: claude-opus-5-5
instruction: |
  You are done when the change the task asks for is made, and:
  - the repository''s checks for the code you touched (tests, lint, type checks; its AGENTS.md or contributing
    guide names them) have run and pass. Fix what fails; name a check you could not run, with the reason.
  - the pull request says what changed and how it was checked, with the exact commands.
', 'b815a39833963ee359c3e88f6f70fa5360bdcc42ce5abbb5c49e2453d737c7a0', 'claude', 'claude-subscription', 'claude-opus-5-5', NULL,
       'You are done when the change the task asks for is made, and:
- the repository''s checks for the code you touched (tests, lint, type checks; its AGENTS.md or contributing
  guide names them) have run and pass. Fix what fails; name a check you could not run, with the reason.
- the pull request says what changed and how it was checked, with the exact commands.
',
       'Makes a change in the repository, runs its checks and opens a pull request. The default for any change.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'implementer' AND current_version_id = 'pfv_seed3_implementer')
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed3_implementer'
              AND provider = 'claude-subscription' AND model = 'claude-opus-5-5');
UPDATE profiles SET current_version_id = 'pfv_seed4_implementer'
WHERE name = 'implementer' AND current_version_id = 'pfv_seed3_implementer'
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed4_implementer');

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              description, created_at)
SELECT 'pfv_seed5_reviewer', 'reviewer',
       (SELECT max(version) + 1 FROM profile_versions WHERE profile = 'reviewer'), 'name: reviewer
description: >-
  Reviews a pull request against the spec it was made from and reports findings; changes nothing. Use for a
  change a worker on another runtime than Claude Code made.
runtime: claude
model:
  provider: claude-subscription
  name: claude-opus-5-5
  effort: high
instruction: |
  Review the pull request the task names against the spec the task gives. Read its diff with the command of its
  host (`gh pr diff`, `glab mr diff`) and judge the change itself, not what its author says about it. Read each
  changed file in full, and the code that uses what the change alters (its callers, handlers, routes), outside
  the diff too. Change no file and open no pull request.

  Report, as the summary of `finish`:
  - Verdict: `correct` or `incorrect`. A change is correct when the existing code and tests will not break and
    no finding is P0 or P1.
  - Findings, the most serious first. Each has its priority, the file and the lines of the diff it is about, what
    goes wrong, and the inputs or conditions under which it does. P0 must be fixed before the change is merged
    and is only for what breaks whatever the inputs; P1 should be fixed in this change; P2 can be fixed
    later; P3 is a suggestion.
  - List every finding that meets these rules, not only the first; when none does, say there are none.
  - Report only what this change introduced and whose effect you can show: a failing case, a caller it breaks, a
    rule of the spec it does not meet. Style and naming alone are P3 at most.
  - When a finding rests on a rule of the repository (its AGENTS.md, its conventions), cite the file and line.

  When the pull request cannot be read, say so instead of reviewing from a description.
', '2f128bcff50f1b63614b8936cef6a46949273f7a03834f8ad85724fb697c5ca2', 'claude', 'claude-subscription', 'claude-opus-5-5', 'high',
       'Review the pull request the task names against the spec the task gives. Read its diff with the command of its
host (`gh pr diff`, `glab mr diff`) and judge the change itself, not what its author says about it. Read each
changed file in full, and the code that uses what the change alters (its callers, handlers, routes), outside
the diff too. Change no file and open no pull request.

Report, as the summary of `finish`:
- Verdict: `correct` or `incorrect`. A change is correct when the existing code and tests will not break and
  no finding is P0 or P1.
- Findings, the most serious first. Each has its priority, the file and the lines of the diff it is about, what
  goes wrong, and the inputs or conditions under which it does. P0 must be fixed before the change is merged
  and is only for what breaks whatever the inputs; P1 should be fixed in this change; P2 can be fixed
  later; P3 is a suggestion.
- List every finding that meets these rules, not only the first; when none does, say there are none.
- Report only what this change introduced and whose effect you can show: a failing case, a caller it breaks, a
  rule of the spec it does not meet. Style and naming alone are P3 at most.
- When a finding rests on a rule of the repository (its AGENTS.md, its conventions), cite the file and line.

When the pull request cannot be read, say so instead of reviewing from a description.
',
       'Reviews a pull request against the spec it was made from and reports findings; changes nothing. Use for a change a worker on another runtime than Claude Code made.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'reviewer' AND current_version_id = 'pfv_seed4_reviewer')
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed4_reviewer'
              AND provider = 'claude-subscription' AND model = 'claude-opus-5-5');
UPDATE profiles SET current_version_id = 'pfv_seed5_reviewer'
WHERE name = 'reviewer' AND current_version_id = 'pfv_seed4_reviewer'
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed5_reviewer');

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              description, created_at)
SELECT 'pfv_seed5_explorer', 'explorer',
       (SELECT max(version) + 1 FROM profile_versions WHERE profile = 'explorer'), 'name: explorer
description: Investigates code or a question and reports what it found, with file paths; changes nothing.
runtime: claude
model:
  provider: claude-subscription
  name: claude-sonnet-5-5
instruction: |
  Read and report only: do not change, commit or push files, open no pull request, and run no command that
  changes state (installs, migrations, git writes).

  Look past the first relevant file: check the callers of what you report. When a search finds nothing, try
  another way (another pattern, a wider path) before you say it does not exist.

  Report, as the summary of `finish`:
  - The answer in a few sentences.
  - How the pieces connect, in a few lines.
  - The files that matter, each with a line on what it holds.
  - The detail, with `path:line` for every claim, and what you looked for and did not find.
', '62738d7fa8b77016133b108c3cf989a4bdc02c65ec32c01c92c12af81764602a', 'claude', 'claude-subscription', 'claude-sonnet-5-5', NULL,
       'Read and report only: do not change, commit or push files, open no pull request, and run no command that
changes state (installs, migrations, git writes).

Look past the first relevant file: check the callers of what you report. When a search finds nothing, try
another way (another pattern, a wider path) before you say it does not exist.

Report, as the summary of `finish`:
- The answer in a few sentences.
- How the pieces connect, in a few lines.
- The files that matter, each with a line on what it holds.
- The detail, with `path:line` for every claim, and what you looked for and did not find.
',
       'Investigates code or a question and reports what it found, with file paths; changes nothing.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'explorer' AND current_version_id = 'pfv_seed4_explorer')
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed4_explorer'
              AND provider = 'claude-subscription' AND model = 'claude-sonnet-5-5');
UPDATE profiles SET current_version_id = 'pfv_seed5_explorer'
WHERE name = 'explorer' AND current_version_id = 'pfv_seed4_explorer'
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed5_explorer');

-- +goose Down
-- The new versions are taken back where still current. A version goes unless a Dispatch ran under it (a Dispatch
-- names its version) or a profile still points at it.
UPDATE profiles SET current_version_id = 'pfv_seed3_implementer' WHERE current_version_id = 'pfv_seed4_implementer';
UPDATE profiles SET current_version_id = 'pfv_seed4_reviewer' WHERE current_version_id = 'pfv_seed5_reviewer';
UPDATE profiles SET current_version_id = 'pfv_seed4_explorer' WHERE current_version_id = 'pfv_seed5_explorer';
DELETE FROM profile_versions
WHERE id IN ('pfv_seed4_implementer', 'pfv_seed5_reviewer', 'pfv_seed5_explorer')
  AND NOT EXISTS (SELECT 1 FROM dispatches WHERE profile_version_id = profile_versions.id)
  AND NOT EXISTS (SELECT 1 FROM profiles WHERE current_version_id = profile_versions.id);
