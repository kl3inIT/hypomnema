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
INSERT INTO gateway_keys (id, name, key_hash, key_prefix, models, endpoints, dispatch_id, chat_id, bot_id, created_at)
VALUES (@id, @name, @key_hash, @key_prefix, @models, @endpoints, @dispatch_id, @chat_id, @bot_id, @now);

-- name: RevokeGatewayKeysOfChat :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE chat_id = @chat_id AND revoked_at IS NULL;

-- name: RevokeGatewayKeysOfDeletedChat :exec
-- A deleted chat's keys (issue #405): revoked and kept, so the usage records stay whole, and they lose the chat, as
-- its Runs do (issue #484).
UPDATE gateway_keys SET revoked_at = coalesce(revoked_at, @now), chat_id = NULL
WHERE chat_id = CAST(@chat_id AS TEXT);

-- name: RevokeGatewayChatKeys :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE chat_id IS NOT NULL AND revoked_at IS NULL;

-- name: RevokeGatewayKeysOfDispatch :execrows
UPDATE gateway_keys SET revoked_at = @now WHERE dispatch_id = @dispatch_id AND revoked_at IS NULL;

-- name: ListDispatchesWithGatewayKeys :many
SELECT DISTINCT dispatch_id FROM gateway_keys WHERE dispatch_id IS NOT NULL AND revoked_at IS NULL;

-- name: GetGatewayKey :one
SELECT * FROM gateway_keys WHERE id = @id;

-- The key a Dispatch's worker uses now: its newest that works (issue #354).
-- name: GetLiveGatewayKeyOfDispatch :one
SELECT * FROM gateway_keys WHERE dispatch_id = @dispatch_id AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC LIMIT 1;

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
                           account_id, error, cache_write_tokens, cache_write_1h_tokens, cost_micros, priced_model,
                           audio_ms, characters, created_at)
VALUES (@id, @key_id, @model, @provider, @upstream_model, @stream, @attempts, @outcome, @status,
        @prompt_tokens, @completion_tokens, @cached_tokens, @reasoning_tokens, @estimated, @duration_ms,
        @account_id, @error, @cache_write_tokens, @cache_write_1h_tokens, @cost_micros, @priced_model,
        @audio_ms, @characters, @now);

-- name: ListGatewayUsage :many
SELECT u.*, k.name AS key_name FROM gateway_usage u JOIN gateway_keys k ON k.id = u.key_id
ORDER BY u.created_at DESC, u.rowid DESC LIMIT @row_limit;

-- The Usage page (issue #199, step 4). A request failed when its outcome is upstream_error, transport_error or
-- truncated; a client that left is not the gateway's failure.

-- name: UsageTotals :one
SELECT CAST(COUNT(*) AS INTEGER) AS requests,
       CAST(COALESCE(SUM(outcome IN ('upstream_error', 'transport_error', 'truncated')), 0) AS INTEGER) AS failed,
       CAST(COALESCE(SUM(prompt_tokens), 0) AS INTEGER) AS prompt_tokens,
       CAST(COALESCE(SUM(cached_tokens), 0) AS INTEGER) AS cached_tokens,
       CAST(COALESCE(SUM(completion_tokens), 0) AS INTEGER) AS completion_tokens,
       CAST(COALESCE(SUM(reasoning_tokens), 0) AS INTEGER) AS reasoning_tokens,
       CAST(COALESCE(SUM(cost_micros), 0) AS INTEGER) AS cost_micros,
       CAST(COALESCE(SUM(cost_micros IS NULL AND prompt_tokens + completion_tokens > 0), 0) AS INTEGER) AS unpriced
FROM gateway_usage WHERE created_at >= @since;

-- name: UsageBuckets :many
SELECT CAST((created_at - @origin) / @bucket_ms AS INTEGER) AS bucket,
       CAST(COUNT(*) AS INTEGER) AS requests,
       CAST(SUM(outcome IN ('upstream_error', 'transport_error', 'truncated')) AS INTEGER) AS failed,
       CAST(SUM(prompt_tokens) AS INTEGER) AS prompt_tokens,
       CAST(SUM(cached_tokens) AS INTEGER) AS cached_tokens,
       CAST(SUM(completion_tokens) AS INTEGER) AS completion_tokens,
       CAST(SUM(reasoning_tokens) AS INTEGER) AS reasoning_tokens,
       CAST(COALESCE(SUM(cost_micros), 0) AS INTEGER) AS cost_micros,
       CAST(COALESCE(SUM(cost_micros IS NULL AND prompt_tokens + completion_tokens > 0), 0) AS INTEGER) AS unpriced
FROM gateway_usage WHERE created_at >= @origin
GROUP BY bucket ORDER BY bucket;

-- name: UsageByModel :many
SELECT model AS name,
       CAST(COUNT(*) AS INTEGER) AS requests,
       CAST(SUM(outcome IN ('upstream_error', 'transport_error', 'truncated')) AS INTEGER) AS failed,
       CAST(SUM(prompt_tokens) AS INTEGER) AS prompt_tokens,
       CAST(SUM(cached_tokens) AS INTEGER) AS cached_tokens,
       CAST(SUM(completion_tokens) AS INTEGER) AS completion_tokens,
       CAST(SUM(reasoning_tokens) AS INTEGER) AS reasoning_tokens,
       CAST(COALESCE(SUM(cost_micros), 0) AS INTEGER) AS cost_micros,
       CAST(COALESCE(SUM(cost_micros IS NULL AND prompt_tokens + completion_tokens > 0), 0) AS INTEGER) AS unpriced
FROM gateway_usage WHERE created_at >= @since
GROUP BY model ORDER BY SUM(prompt_tokens + completion_tokens) DESC, model;

-- name: UsageByKey :many
-- A key the control plane made for a chat's pi counts under the chat's Bot, and one made for a worker's Dispatch
-- under the workers (issue #626): each pi and each Dispatch gets a new key, named at random. A chat key made before
-- keys named their Bot, whose chat was deleted since, is told by its name alone (chat- or dispatch- and an id).
-- The owner's own keys count each under its name.
SELECT CAST(CASE
         WHEN k.bot_id IS NOT NULL THEN k.bot_id
         WHEN k.dispatch_id IS NOT NULL OR (k.name LIKE 'dispatch-%' AND length(k.name) = 35) THEN 'workers'
         WHEN k.chat_id IS NOT NULL OR (k.name LIKE 'chat-%' AND length(k.name) = 31) THEN 'chats'
         ELSE k.id END AS TEXT) AS id,
       CAST(CASE
         WHEN k.bot_id IS NOT NULL OR k.dispatch_id IS NOT NULL OR k.chat_id IS NOT NULL
           OR (k.name LIKE 'dispatch-%' AND length(k.name) = 35) OR (k.name LIKE 'chat-%' AND length(k.name) = 31)
           THEN '' ELSE k.name END AS TEXT) AS name,
       CAST(COUNT(*) AS INTEGER) AS requests,
       CAST(SUM(u.outcome IN ('upstream_error', 'transport_error', 'truncated')) AS INTEGER) AS failed,
       CAST(SUM(u.prompt_tokens) AS INTEGER) AS prompt_tokens,
       CAST(SUM(u.cached_tokens) AS INTEGER) AS cached_tokens,
       CAST(SUM(u.completion_tokens) AS INTEGER) AS completion_tokens,
       CAST(SUM(u.reasoning_tokens) AS INTEGER) AS reasoning_tokens,
       CAST(COALESCE(SUM(u.cost_micros), 0) AS INTEGER) AS cost_micros,
       CAST(COALESCE(SUM(u.cost_micros IS NULL AND u.prompt_tokens + u.completion_tokens > 0), 0) AS INTEGER) AS unpriced
FROM gateway_usage u JOIN gateway_keys k ON k.id = u.key_id WHERE u.created_at >= @since
GROUP BY 1, 2 ORDER BY SUM(u.prompt_tokens + u.completion_tokens) DESC, 2, 1;

-- name: UsageByAccount :many
SELECT account_id AS id,
       CAST(COUNT(*) AS INTEGER) AS requests,
       CAST(SUM(outcome IN ('upstream_error', 'transport_error', 'truncated')) AS INTEGER) AS failed,
       CAST(SUM(prompt_tokens) AS INTEGER) AS prompt_tokens,
       CAST(SUM(cached_tokens) AS INTEGER) AS cached_tokens,
       CAST(SUM(completion_tokens) AS INTEGER) AS completion_tokens,
       CAST(SUM(reasoning_tokens) AS INTEGER) AS reasoning_tokens,
       CAST(COALESCE(SUM(cost_micros), 0) AS INTEGER) AS cost_micros,
       CAST(COALESCE(SUM(cost_micros IS NULL AND prompt_tokens + completion_tokens > 0), 0) AS INTEGER) AS unpriced
FROM gateway_usage WHERE created_at >= @since AND account_id != ''
GROUP BY account_id ORDER BY SUM(prompt_tokens + completion_tokens) DESC, account_id;

-- name: ListUsageRequests :many
SELECT u.*, k.name AS key_name FROM gateway_usage u JOIN gateway_keys k ON k.id = u.key_id
WHERE u.created_at >= @since
  AND (CAST(@failed_only AS INTEGER) = 0 OR u.outcome IN ('upstream_error', 'transport_error', 'truncated'))
ORDER BY u.created_at DESC, u.rowid DESC LIMIT @row_limit OFFSET @row_offset;

-- What the owner set for the web search providers (issues #354, #393).
-- name: ListGatewaySearchProviders :many
SELECT * FROM gateway_search_providers ORDER BY provider;

-- name: SetGatewaySearchOff :exec
INSERT INTO gateway_search_providers (provider, off, updated_at) VALUES (@provider, @off, @now)
ON CONFLICT (provider) DO UPDATE SET off = excluded.off, updated_at = excluded.updated_at;

-- name: SetGatewaySearchPosition :exec
INSERT INTO gateway_search_providers (provider, position, updated_at) VALUES (@provider, @position, @now)
ON CONFLICT (provider) DO UPDATE SET position = excluded.position, updated_at = excluded.updated_at;

-- Each web search call, counted by its UTC day (issue #393).
-- name: CountGatewaySearch :exec
INSERT INTO gateway_search_usage (provider, key, day, answered, failed, cost_micros)
VALUES (@provider, @key, @day, @answered, @failed, @cost_micros)
ON CONFLICT (provider, key, day) DO UPDATE SET
    answered = answered + excluded.answered, failed = failed + excluded.failed,
    cost_micros = cost_micros + excluded.cost_micros;

-- name: PruneGatewaySearchUsage :exec
DELETE FROM gateway_search_usage WHERE day < @before;

-- name: ListGatewaySearchUsage :many
SELECT * FROM gateway_search_usage WHERE day >= @since ORDER BY provider, key, day;

-- What the owner set on Settings, Gateway, Images (issue #431).
-- name: ListGatewayImageProviders :many
SELECT * FROM gateway_image_providers ORDER BY provider;

-- name: SetGatewayImageOff :exec
INSERT INTO gateway_image_providers (provider, off, updated_at) VALUES (@provider, @off, @now)
ON CONFLICT (provider) DO UPDATE SET off = excluded.off, updated_at = excluded.updated_at;

-- name: SetGatewayImagePosition :exec
INSERT INTO gateway_image_providers (provider, position, updated_at) VALUES (@provider, @position, @now)
ON CONFLICT (provider) DO UPDATE SET position = excluded.position, updated_at = excluded.updated_at;

-- Each image provider's requests since a moment: what answered and what failed, for the Images page.
-- name: CountGatewayImageCalls :many
SELECT provider,
       CAST(SUM(outcome = 'ok') AS INTEGER) AS answered,
       CAST(SUM(outcome IN ('upstream_error', 'transport_error', 'truncated')) AS INTEGER) AS failed
FROM gateway_usage WHERE model LIKE 'image/%' AND created_at >= @since
GROUP BY provider ORDER BY provider;

-- What the owner set on Settings, Gateway, Audio (issue #694).
-- name: ListGatewayAudioProviders :many
SELECT * FROM gateway_audio_providers ORDER BY provider;

-- name: SetGatewayAudioOff :exec
INSERT INTO gateway_audio_providers (provider, off, updated_at) VALUES (@provider, @off, @now)
ON CONFLICT (provider) DO UPDATE SET off = excluded.off, updated_at = excluded.updated_at;

-- name: SetGatewayAudioSTTPosition :exec
INSERT INTO gateway_audio_providers (provider, stt_position, updated_at) VALUES (@provider, @position, @now)
ON CONFLICT (provider) DO UPDATE SET stt_position = excluded.stt_position, updated_at = excluded.updated_at;

-- name: SetGatewayAudioTTSPosition :exec
INSERT INTO gateway_audio_providers (provider, tts_position, updated_at) VALUES (@provider, @position, @now)
ON CONFLICT (provider) DO UPDATE SET tts_position = excluded.tts_position, updated_at = excluded.updated_at;

-- Each audio provider's requests since a moment, by the model it was asked with (one model a provider and kind):
-- what answered, what failed, the milliseconds transcribed and the characters spoken, for the Audio page.
-- name: CountGatewayAudioCalls :many
SELECT provider, model,
       CAST(SUM(outcome = 'ok') AS INTEGER) AS answered,
       CAST(SUM(outcome IN ('upstream_error', 'transport_error', 'truncated')) AS INTEGER) AS failed,
       CAST(COALESCE(SUM(audio_ms), 0) AS INTEGER) AS audio_ms,
       CAST(COALESCE(SUM(characters), 0) AS INTEGER) AS characters
FROM gateway_usage WHERE model LIKE 'audio/%' AND created_at >= @since
GROUP BY provider, model ORDER BY provider, model;

-- name: ListAntigravityRewrites :many
SELECT position, find, replacement FROM antigravity_prompt_rewrites ORDER BY position;

-- name: DeleteAntigravityRewrites :exec
DELETE FROM antigravity_prompt_rewrites;

-- name: InsertAntigravityRewrite :exec
INSERT INTO antigravity_prompt_rewrites (position, find, replacement, updated_at)
VALUES (@position, @find, @replacement, @now);

-- name: DeleteGatewayUsageBefore :execrows
DELETE FROM gateway_usage WHERE created_at < @before;

-- name: DeleteUnusedRevokedGatewayKeys :execrows
-- A revoked key no request names (issue #628); a live key is never deleted.
DELETE FROM gateway_keys
WHERE revoked_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM gateway_usage u WHERE u.key_id = gateway_keys.id);
