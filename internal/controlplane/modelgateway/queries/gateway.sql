-- name: UpsertGatewayModel :exec
INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
VALUES (@name, @capability, @source, @now, @now)
ON CONFLICT (name) DO UPDATE SET capability = excluded.capability, source = excluded.source,
                                 updated_at = excluded.updated_at;

-- name: DeleteGatewayModelTargets :exec
DELETE FROM gateway_model_targets WHERE model = @model;

-- name: InsertGatewayModelTarget :exec
INSERT INTO gateway_model_targets (model, position, provider, upstream_model)
VALUES (@model, @position, @provider, @upstream_model);

-- name: GetGatewayModel :one
SELECT * FROM gateway_models WHERE name = @name;

-- name: ListGatewayModels :many
SELECT * FROM gateway_models ORDER BY name;

-- name: ListGatewayModelTargets :many
SELECT * FROM gateway_model_targets ORDER BY model, position;

-- name: ListGatewayModelTargetsOf :many
SELECT * FROM gateway_model_targets WHERE model = @model ORDER BY position;

-- name: DeleteGatewayModel :execrows
DELETE FROM gateway_models WHERE name = @name;

-- name: InsertGatewayKey :exec
INSERT INTO gateway_keys (id, name, key_hash, models, created_at) VALUES (@id, @name, @key_hash, @models, @now);

-- name: GetGatewayKey :one
SELECT * FROM gateway_keys WHERE id = @id;

-- name: GetGatewayKeyByHash :one
SELECT * FROM gateway_keys WHERE key_hash = @key_hash;

-- name: ListGatewayKeys :many
SELECT * FROM gateway_keys ORDER BY name;

-- name: RevokeGatewayKey :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE id = @id AND revoked_at IS NULL;
