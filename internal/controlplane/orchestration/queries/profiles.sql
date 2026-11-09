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
SELECT * FROM profiles WHERE name = @name;

-- name: InsertProfile :exec
INSERT INTO profiles (name, current_version_id, created_at, updated_at) VALUES (@name, @version_id, @now, @now);

-- name: SetProfileVersion :exec
UPDATE profiles SET current_version_id = @version_id, updated_at = @now WHERE name = @name;

-- name: InsertProfileVersion :exec
INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort, instruction,
                              created_at)
VALUES (@id, @profile, @version, @source, @digest, @runtime, @provider, @model, @effort, @instruction, @now);

-- name: GetProfileVersion :one
SELECT * FROM profile_versions WHERE id = @id;

-- name: CurrentProfileVersion :one
SELECT v.* FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id WHERE p.name = @name;

-- name: ListCurrentProfileVersions :many
SELECT v.* FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id ORDER BY p.name;

-- name: ListProfileVersions :many
SELECT * FROM profile_versions WHERE profile = @profile ORDER BY version DESC;
