-- A fresh install's starting profiles run Claude Code through the model gateway (issue #462). Since #381 a Claude
-- Code worker runs only through the gateway, and the starting set (00008, 00009, 00040) named no provider, so it
-- waited with needs_gateway until the owner applied a provider, gateway models and new profile versions by hand.
-- As 00008 seeds providers and profiles and 00024 and 00027 add a built-in provider only when it is not there, this
-- seeds:
--
-- - the provider `model-gateway`, of kind model_gateway, with three Claude models, only when no provider of that
--   kind exists;
-- - gateway models of those names, each served from the Claude subscription's model of the same name, only for a
--   name not taken and a model the subscription offers (what ApplyModel checks);
-- - as 00009 and 00040 do, a new version of `implementer` and `reviewer` on claude-opus-5-5 and of `explorer` on
--   claude-sonnet-5-5 through that provider, only where the profile's current version is still the seed's latest
--   (pfv_seed2_implementer, pfv_seed3_reviewer, pfv_seed3_explorer) and the provider offers the model. Each keeps
--   its seed's description, instruction and effort; the seeds had no fallbacks or skills. Source and digest are what
--   ApplyProfile writes for that YAML, so applying the same YAML later makes no new version.
--
-- Rows written here have updated_at 0, as every seed; ApplyProvider and ApplyModel set it to the time they apply, so
-- the way back tells what this migration made and the owner has not changed. Models chosen as the owner chose them
-- on zm (owner, 2026-11-14).

-- +goose Up
INSERT INTO providers (name, kind, source, created_at, updated_at)
SELECT 'model-gateway', 'model_gateway',
       'name: model-gateway
kind: model_gateway
models: [claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5]
', 0, 0
WHERE NOT EXISTS (SELECT 1 FROM providers WHERE kind = 'model_gateway' OR name = 'model-gateway');

INSERT OR IGNORE INTO models (provider, name)
SELECT p.name, m.name
FROM providers p,
     (SELECT 'claude-opus-5-5' AS name UNION ALL SELECT 'claude-sonnet-5-5' UNION ALL SELECT 'claude-haiku-5-5') AS m
WHERE p.name = 'model-gateway' AND p.kind = 'model_gateway' AND p.updated_at = 0;

CREATE TEMP TABLE gateway_seed (name TEXT NOT NULL PRIMARY KEY, source TEXT NOT NULL);

INSERT INTO gateway_seed (name, source)
VALUES ('claude-opus-5-5', 'name: claude-opus-5-5
targets:
  - {provider: claude-subscription, model: claude-opus-5-5}
'),
       ('claude-sonnet-5-5', 'name: claude-sonnet-5-5
targets:
  - {provider: claude-subscription, model: claude-sonnet-5-5}
'),
       ('claude-haiku-5-5', 'name: claude-haiku-5-5
targets:
  - {provider: claude-subscription, model: claude-haiku-5-5}
');

DELETE FROM gateway_seed
WHERE name IN (SELECT name FROM gateway_models)
   OR NOT EXISTS (
       SELECT 1 FROM providers p JOIN models m ON m.provider = p.name
       WHERE p.name = 'claude-subscription' AND p.kind = 'claude_subscription' AND m.name = gateway_seed.name);

INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
SELECT name, 'chat', source, 0, 0 FROM gateway_seed;

INSERT INTO gateway_model_targets (model, position, provider, upstream_model)
SELECT name, 0, 'claude-subscription', name FROM gateway_seed;

DROP TABLE gateway_seed;

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              description, created_at)
SELECT 'pfv_seed3_implementer', 'implementer', 3, 'name: implementer
description: Makes a change in the repository, runs its checks and opens a pull request. The default for any change.
runtime: claude
model:
  provider: model-gateway
  name: claude-opus-5-5
', '961b7c708025e6cb22727267575e64492271dec3f11936be8cbf78f47000cbf2', 'claude', 'model-gateway', 'claude-opus-5-5', NULL, NULL,
       'Makes a change in the repository, runs its checks and opens a pull request. The default for any change.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'implementer' AND current_version_id = 'pfv_seed2_implementer')
  AND EXISTS (SELECT 1 FROM providers p JOIN models m ON m.provider = p.name
              WHERE p.name = 'model-gateway' AND p.kind = 'model_gateway' AND m.name = 'claude-opus-5-5');
UPDATE profiles SET current_version_id = 'pfv_seed3_implementer'
WHERE name = 'implementer' AND current_version_id = 'pfv_seed2_implementer'
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed3_implementer');

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              description, created_at)
SELECT 'pfv_seed4_reviewer', 'reviewer', 4, 'name: reviewer
description: >-
  Reviews a pull request against the spec it was made from and reports findings; changes nothing. Use for a
  change a worker on another runtime than Claude Code made.
runtime: claude
model:
  provider: model-gateway
  name: claude-opus-5-5
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
', '3fe47b47806540c1b4d2233b12510285bc641a47c357ece48791e178311b5413', 'claude', 'model-gateway', 'claude-opus-5-5', 'high',
       'Review the pull request the task names against the spec the task gives. Read its diff with the command of its
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
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'reviewer' AND current_version_id = 'pfv_seed3_reviewer')
  AND EXISTS (SELECT 1 FROM providers p JOIN models m ON m.provider = p.name
              WHERE p.name = 'model-gateway' AND p.kind = 'model_gateway' AND m.name = 'claude-opus-5-5');
UPDATE profiles SET current_version_id = 'pfv_seed4_reviewer'
WHERE name = 'reviewer' AND current_version_id = 'pfv_seed3_reviewer'
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed4_reviewer');

INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              description, created_at)
SELECT 'pfv_seed4_explorer', 'explorer', 4, 'name: explorer
description: Investigates code or a question and reports what it found, with file paths; changes nothing.
runtime: claude
model:
  provider: model-gateway
  name: claude-sonnet-5-5
instruction: |
  Read and report only: do not change, commit or push files, and open no pull request.

  Report, as the summary of `finish`:
  - The answer in a few sentences.
  - The files that matter, each with a line on what it holds.
  - The detail, with `path:line` for every claim, and what you looked for and did not find.
', '0ee2609075fa724167c37aafde8e853cd2c6caaaede51172680b42d36ee16dfc', 'claude', 'model-gateway', 'claude-sonnet-5-5', NULL,
       'Read and report only: do not change, commit or push files, and open no pull request.

Report, as the summary of `finish`:
- The answer in a few sentences.
- The files that matter, each with a line on what it holds.
- The detail, with `path:line` for every claim, and what you looked for and did not find.
',
       'Investigates code or a question and reports what it found, with file paths; changes nothing.', 0
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'explorer' AND current_version_id = 'pfv_seed3_explorer')
  AND EXISTS (SELECT 1 FROM providers p JOIN models m ON m.provider = p.name
              WHERE p.name = 'model-gateway' AND p.kind = 'model_gateway' AND m.name = 'claude-sonnet-5-5');
UPDATE profiles SET current_version_id = 'pfv_seed4_explorer'
WHERE name = 'explorer' AND current_version_id = 'pfv_seed3_explorer'
  AND EXISTS (SELECT 1 FROM profile_versions WHERE id = 'pfv_seed4_explorer');

-- +goose Down
-- The seed versions are taken back where still current. A version goes unless a Dispatch ran under it (a Dispatch
-- names its version) or a profile still points at it.
UPDATE profiles SET current_version_id = 'pfv_seed2_implementer' WHERE current_version_id = 'pfv_seed3_implementer';
UPDATE profiles SET current_version_id = 'pfv_seed3_reviewer' WHERE current_version_id = 'pfv_seed4_reviewer';
UPDATE profiles SET current_version_id = 'pfv_seed3_explorer' WHERE current_version_id = 'pfv_seed4_explorer';
DELETE FROM profile_versions
WHERE id IN ('pfv_seed3_implementer', 'pfv_seed4_reviewer', 'pfv_seed4_explorer')
  AND NOT EXISTS (SELECT 1 FROM dispatches WHERE profile_version_id = profile_versions.id)
  AND NOT EXISTS (SELECT 1 FROM profiles WHERE current_version_id = profile_versions.id);

-- A gateway model this migration made and the owner has not applied again goes, with its target, unless a Profile
-- version, a Bot or a chat names it on a model_gateway provider, or a key that works lists it by name.
DELETE FROM gateway_models
WHERE name IN ('claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5') AND updated_at = 0
  AND NOT EXISTS (SELECT 1 FROM profile_versions v JOIN providers p ON p.name = v.provider
                  WHERE p.kind = 'model_gateway' AND v.model = gateway_models.name)
  AND NOT EXISTS (SELECT 1 FROM bots b JOIN providers p ON p.name = b.provider
                  WHERE p.kind = 'model_gateway' AND b.model = gateway_models.name)
  AND NOT EXISTS (SELECT 1 FROM chats c JOIN providers p ON p.name = c.provider
                  WHERE p.kind = 'model_gateway' AND c.model = gateway_models.name)
  AND NOT EXISTS (SELECT 1 FROM gateway_keys k, json_each(k.models) j
                  WHERE k.revoked_at IS NULL AND j.value = gateway_models.name);

-- The provider, as this migration made it, goes with its models (ON DELETE CASCADE) when nothing names it.
DELETE FROM providers
WHERE name = 'model-gateway' AND kind = 'model_gateway' AND updated_at = 0
  AND NOT EXISTS (SELECT 1 FROM profile_versions WHERE provider = 'model-gateway')
  AND NOT EXISTS (SELECT 1 FROM bots WHERE provider = 'model-gateway')
  AND NOT EXISTS (SELECT 1 FROM chats WHERE provider = 'model-gateway')
  AND NOT EXISTS (SELECT 1 FROM gateway_model_targets WHERE provider = 'model-gateway');
