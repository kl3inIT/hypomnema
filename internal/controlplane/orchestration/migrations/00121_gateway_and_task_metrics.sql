-- Speed, reliability and cost per provider, per Task and per Run (issue #856).
--
-- gateway_usage.ttft_ms is the time to first token: from the gateway receiving the request to the first chunk of the
-- answer it streams to the client, as LiteLLM's completion_start_time and Helicone's first chunk
-- (ProxyRequestHandler.ts:207-215). NULL for an answer that is not a stream, and for a stream that sent nothing.
--
-- gateway_usage_daily is the ledger the 31-day purge of gateway_usage (issue #628) never touches, as LiteLLM's
-- LiteLLM_DailyUserSpend beside its spend logs (schema.prisma:865-887): one row a UTC day (its midnight, in Unix
-- milliseconds), caller, model, provider, upstream model and account, upserted with each request row in the same
-- transaction. The caller is the group the Usage page counts a key under (UsageByKey): a Bot's id, workers, chats
-- or owner; the ledger names no key, so a revoked key can still be deleted. The speed columns count the requests
-- that were answered (outcome ok): the sum of their durations, the sum and count of their times to first token, and
-- the generation time (duration less time to first token) and output tokens of those whose tokens were reported
-- and whose generation took time, for tokens a second. Read and written only by the model gateway.
--
-- dispatch_usage is what the gateway served a Dispatch through its keys, written when the Dispatch settles (Omnigent
-- per-session cost, 9router-go's prune that carries the spend of the rows it removes): a Task's cost survives the
-- purge of the request rows. Written and read by orchestration, which reads gateway_usage and gateway_keys for it.

-- +goose Up
ALTER TABLE gateway_usage ADD COLUMN ttft_ms INTEGER;

CREATE TABLE gateway_usage_daily (
    day                INTEGER NOT NULL,
    caller             TEXT    NOT NULL,
    model              TEXT    NOT NULL,
    provider           TEXT    NOT NULL,
    upstream_model     TEXT    NOT NULL,
    account_id         TEXT    NOT NULL,
    requests           INTEGER NOT NULL DEFAULT 0,
    ok                 INTEGER NOT NULL DEFAULT 0,
    failed             INTEGER NOT NULL DEFAULT 0,
    client_gone        INTEGER NOT NULL DEFAULT 0,
    prompt_tokens      INTEGER NOT NULL DEFAULT 0,
    completion_tokens  INTEGER NOT NULL DEFAULT 0,
    cached_tokens      INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    web_searches       INTEGER NOT NULL DEFAULT 0,
    audio_ms           INTEGER NOT NULL DEFAULT 0,
    characters         INTEGER NOT NULL DEFAULT 0,
    images             INTEGER NOT NULL DEFAULT 0,
    cost_micros        INTEGER NOT NULL DEFAULT 0,
    unpriced           INTEGER NOT NULL DEFAULT 0,
    ok_duration_ms     INTEGER NOT NULL DEFAULT 0,
    ttft_ms            INTEGER NOT NULL DEFAULT 0,
    ttft_count         INTEGER NOT NULL DEFAULT 0,
    generation_ms      INTEGER NOT NULL DEFAULT 0,
    generation_tokens  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, caller, model, provider, upstream_model, account_id)
) STRICT;

CREATE TABLE dispatch_usage (
    dispatch_id        TEXT    NOT NULL PRIMARY KEY REFERENCES dispatches (id),
    requests           INTEGER NOT NULL,
    prompt_tokens      INTEGER NOT NULL,
    completion_tokens  INTEGER NOT NULL,
    cached_tokens      INTEGER NOT NULL,
    reasoning_tokens   INTEGER NOT NULL,
    cache_write_tokens INTEGER NOT NULL,
    cost_micros        INTEGER NOT NULL,
    unpriced           INTEGER NOT NULL,
    recorded_at        INTEGER NOT NULL
) STRICT;

-- The ledger and the Dispatches' totals start from the request rows still kept.
INSERT INTO gateway_usage_daily (day, caller, model, provider, upstream_model, account_id, requests, ok, failed,
                                 client_gone, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens,
                                 cache_write_tokens, web_searches, audio_ms, characters, images, cost_micros, unpriced,
                                 ok_duration_ms, ttft_ms, ttft_count, generation_ms, generation_tokens)
SELECT u.created_at - u.created_at % 86400000,
       CASE
           WHEN k.bot_id IS NOT NULL THEN k.bot_id
           WHEN k.dispatch_id IS NOT NULL OR (k.name LIKE 'dispatch-%' AND length(k.name) = 35) THEN 'workers'
           WHEN k.chat_id IS NOT NULL OR (k.name LIKE 'chat-%' AND length(k.name) = 31) THEN 'chats'
           ELSE 'owner' END,
       u.model, u.provider, u.upstream_model, u.account_id,
       COUNT(*),
       SUM(u.outcome = 'ok'),
       SUM(u.outcome IN ('upstream_error', 'transport_error', 'truncated')),
       SUM(u.outcome = 'client_gone'),
       SUM(u.prompt_tokens), SUM(u.completion_tokens), SUM(u.cached_tokens), SUM(u.reasoning_tokens),
       SUM(u.cache_write_tokens), SUM(u.web_searches), SUM(u.audio_ms), SUM(u.characters), SUM(u.images),
       COALESCE(SUM(u.cost_micros), 0),
       SUM(u.cost_micros IS NULL AND u.prompt_tokens + u.completion_tokens + u.web_searches + u.audio_ms + u.characters > 0),
       SUM(CASE WHEN u.outcome = 'ok' THEN u.duration_ms ELSE 0 END),
       0, 0, 0, 0
FROM gateway_usage u JOIN gateway_keys k ON k.id = u.key_id
GROUP BY 1, 2, 3, 4, 5, 6;

INSERT INTO dispatch_usage (dispatch_id, requests, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens,
                            cache_write_tokens, cost_micros, unpriced, recorded_at)
SELECT d.id, COUNT(*), SUM(u.prompt_tokens), SUM(u.completion_tokens), SUM(u.cached_tokens), SUM(u.reasoning_tokens),
       SUM(u.cache_write_tokens), COALESCE(SUM(u.cost_micros), 0),
       SUM(u.cost_micros IS NULL AND u.prompt_tokens + u.completion_tokens + u.web_searches + u.audio_ms + u.characters > 0),
       COALESCE(d.settled_at, d.updated_at)
FROM dispatches d
JOIN gateway_keys k ON k.dispatch_id = d.id
JOIN gateway_usage u ON u.key_id = k.id
WHERE d.status IN ('succeeded', 'failed', 'gated', 'canceled', 'expired', 'abandoned')
GROUP BY d.id;

-- +goose Down
DROP TABLE dispatch_usage;
DROP TABLE gateway_usage_daily;
ALTER TABLE gateway_usage DROP COLUMN ttft_ms;
