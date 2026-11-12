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
INSERT INTO gateway_keys (id, name, key_hash, key_prefix, models, dispatch_id, chat_id, created_at)
VALUES (@id, @name, @key_hash, @key_prefix, @models, @dispatch_id, @chat_id, @now);

-- name: RevokeGatewayKeysOfChat :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE chat_id = @chat_id AND revoked_at IS NULL;

-- name: RevokeGatewayChatKeys :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE chat_id IS NOT NULL AND revoked_at IS NULL;

-- name: RevokeGatewayKeysOfDispatch :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE dispatch_id = @dispatch_id AND revoked_at IS NULL;

-- name: ListDispatchesWithGatewayKeys :many
SELECT DISTINCT dispatch_id FROM gateway_keys WHERE dispatch_id IS NOT NULL AND revoked_at IS NULL;

-- name: GetGatewayKey :one
SELECT * FROM gateway_keys WHERE id = @id;

-- name: GetGatewayKeyByHash :one
SELECT * FROM gateway_keys WHERE key_hash = @key_hash;

-- name: ListGatewayKeys :many
SELECT * FROM gateway_keys ORDER BY name;

-- name: GatewayKeysLastUsed :many
SELECT key_id, CAST(MAX(created_at) AS INTEGER) AS last_used FROM gateway_usage GROUP BY key_id;

-- name: RevokeGatewayKey :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE id = @id AND revoked_at IS NULL;

-- name: InsertGatewayUsage :exec
INSERT INTO gateway_usage (id, key_id, model, provider, upstream_model, stream, attempts, outcome, status,
                           prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, estimated, duration_ms,
                           created_at)
VALUES (@id, @key_id, @model, @provider, @upstream_model, @stream, @attempts, @outcome, @status,
        @prompt_tokens, @completion_tokens, @cached_tokens, @reasoning_tokens, @estimated, @duration_ms, @now);

-- name: ListGatewayUsage :many
SELECT u.*, k.name AS key_name FROM gateway_usage u JOIN gateway_keys k ON k.id = u.key_id
ORDER BY u.created_at DESC, u.id DESC LIMIT @row_limit;
