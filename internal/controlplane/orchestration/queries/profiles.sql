-- name: UpsertProvider :exec
INSERT INTO providers (name, kind, base_url, api_key_ref, source, created_at, updated_at)
VALUES (@name, @kind, @base_url, @api_key_ref, @source, @now, @now)
ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, base_url = excluded.base_url,
                                 api_key_ref = excluded.api_key_ref, source = excluded.source,
                                 updated_at = excluded.updated_at;

-- name: GetProvider :one
SELECT * FROM providers WHERE name = @name;

-- name: ListProviders :many
SELECT * FROM providers ORDER BY name;

-- name: DeleteModelsOfProvider :exec
DELETE FROM models WHERE provider = @provider;

-- name: InsertModel :exec
INSERT INTO models (provider, name) VALUES (@provider, @name);

-- name: ListModels :many
SELECT m.provider, m.name, p.kind FROM models m JOIN providers p ON p.name = m.provider
ORDER BY m.provider, m.name;

-- name: ModelsNamed :many
SELECT m.provider, m.name, p.kind FROM models m JOIN providers p ON p.name = m.provider
WHERE m.name = @name ORDER BY m.provider;

-- name: GetProfile :one
-- A profile that is not deleted.
SELECT * FROM profiles WHERE name = @name AND deleted_at IS NULL;

-- name: GetProfileEvenDeleted :one
SELECT * FROM profiles WHERE name = @name;

-- name: InsertProfile :exec
INSERT INTO profiles (name, current_version_id, created_at, updated_at) VALUES (@name, @version_id, @now, @now);

-- name: SetProfileVersion :exec
-- A new version of a deleted profile brings it back (issue #485).
UPDATE profiles SET current_version_id = @version_id, deleted_at = NULL, updated_at = @now WHERE name = @name;

-- name: MarkProfileDeleted :exec
UPDATE profiles SET updated_at = @now, deleted_at = @now WHERE name = @name;

-- name: InsertProfileVersion :exec
INSERT INTO profile_versions (id, profile, version, source, digest, description, runtime, provider, model, effort,
                              instructions, fallbacks, skills, skills_off, tools, tools_off, cpu_request_millis,
                              memory_request_bytes, preset, created_at)
VALUES (@id, @profile, @version, @source, @digest, @description, @runtime, @provider, @model, @effort, @instructions,
        @fallbacks, @skills, @skills_off, @tools, @tools_off, @cpu_request_millis, @memory_request_bytes, @preset, @now);

-- name: GetProfileVersion :one
SELECT * FROM profile_versions WHERE id = @id;

-- name: CurrentProfileVersion :one
-- The current version of a profile that is not deleted.
SELECT v.* FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id
WHERE p.name = @name AND p.deleted_at IS NULL;

-- name: ListCurrentProfileVersions :many
SELECT v.* FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id
WHERE p.deleted_at IS NULL ORDER BY p.name;

-- name: ListProfileVersions :many
SELECT * FROM profile_versions WHERE profile = @profile ORDER BY version DESC;

-- name: ListProviderNamesByKeyRef :many
SELECT name FROM providers WHERE api_key_ref = @api_key_ref ORDER BY name;

-- name: CountTasksOfProfileSince :one
SELECT count(*) FROM tasks WHERE profile = @profile AND created_at >= @since;

-- name: DeleteProfileVersions :exec
DELETE FROM profile_versions WHERE profile = @profile;

-- name: DeleteProfile :exec
DELETE FROM profiles WHERE name = @name;

-- name: CountDispatchesOfProfileVersion :one
SELECT count(*) FROM dispatches WHERE profile_version_id = @version_id;

-- name: CountDispatchesOfProfile :one
-- The Dispatches that ran any version of a profile.
SELECT count(*) FROM dispatches d JOIN profile_versions v ON v.id = d.profile_version_id WHERE v.profile = @profile;

-- name: CountUnfinishedTasksOfProfile :one
-- The Tasks that name a profile and have not ended, a failed one of an open Run included: the owner may reopen it.
SELECT count(*) FROM tasks t JOIN runs r ON r.id = t.run_id
WHERE t.profile = @profile
  AND (t.status NOT IN ('succeeded', 'failed', 'canceled') OR (t.status = 'failed' AND r.status = 'open'));

-- name: BotsNamingWorker :many
-- The Bots whose workers list names a profile.
SELECT name FROM bots WHERE workers IS NOT NULL AND ',' || workers || ',' LIKE '%,' || @profile || ',%' ORDER BY name;

-- name: ToolSwitchesOfDispatch :one
SELECT coalesce(v.preset, 'full') AS preset, coalesce(v.tools, '') AS tools, coalesce(v.tools_off, '') AS tools_off,
       coalesce(r.bot_id, '') AS bot_id, coalesce(b.preset, '') AS bot_preset,
       coalesce(b.tools, '') AS bot_tools, coalesce(b.tools_off, '') AS bot_tools_off
FROM dispatches d JOIN tasks t ON t.id = d.task_id JOIN runs r ON r.id = t.run_id
LEFT JOIN profile_versions v ON v.id = d.profile_version_id LEFT JOIN bots b ON b.id = r.bot_id
WHERE d.id = @dispatch_id;

-- name: DeleteProvider :execrows
DELETE FROM providers WHERE name = @name;

-- name: ProviderUses :one
SELECT
  (SELECT count(*) FROM bots WHERE bots.provider = sqlc.arg(pname)) AS bots,
  (SELECT count(*) FROM chats WHERE chats.provider = sqlc.arg(pname)) AS chats,
  (SELECT count(*) FROM profile_versions WHERE profile_versions.provider = sqlc.arg(pname)) AS versions,
  (SELECT count(*) FROM gateway_model_targets WHERE gateway_model_targets.provider = sqlc.arg(pname)) AS targets;
