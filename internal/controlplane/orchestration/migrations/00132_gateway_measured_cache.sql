-- +goose Up
ALTER TABLE gateway_usage_daily ADD COLUMN estimated_requests INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_usage_daily ADD COLUMN estimated_prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_usage_daily ADD COLUMN measured_prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_usage_daily ADD COLUMN measured_cached_tokens INTEGER NOT NULL DEFAULT 0;

-- Only surviving requests can be classified. Keep the old token and cost totals even for purged requests.
WITH split AS (
    SELECT u.created_at - u.created_at % 86400000 AS day,
           CASE
               WHEN k.bot_id IS NOT NULL THEN k.bot_id
               WHEN k.dispatch_id IS NOT NULL OR (k.name LIKE 'dispatch-%' AND length(k.name) = 35) THEN 'workers'
               WHEN k.chat_id IS NOT NULL OR (k.name LIKE 'chat-%' AND length(k.name) = 31) THEN 'chats'
               ELSE 'owner' END AS caller,
           u.model, u.provider, u.upstream_model, u.account_id,
           SUM(u.estimated = 1) AS estimated_requests,
           SUM(CASE WHEN u.estimated = 1 THEN u.prompt_tokens ELSE 0 END) AS estimated_prompt_tokens,
           SUM(CASE WHEN u.estimated = 0 THEN u.prompt_tokens ELSE 0 END) AS measured_prompt_tokens,
           SUM(CASE WHEN u.estimated = 0 THEN u.cached_tokens ELSE 0 END) AS measured_cached_tokens
    FROM gateway_usage u JOIN gateway_keys k ON k.id = u.key_id
    GROUP BY day, caller, u.model, u.provider, u.upstream_model, u.account_id
)
UPDATE gateway_usage_daily
SET (estimated_requests, estimated_prompt_tokens, measured_prompt_tokens, measured_cached_tokens) = (
    SELECT estimated_requests, estimated_prompt_tokens, measured_prompt_tokens, measured_cached_tokens FROM split
    WHERE (split.day, split.caller, split.model, split.provider, split.upstream_model, split.account_id) =
          (gateway_usage_daily.day, gateway_usage_daily.caller, gateway_usage_daily.model, gateway_usage_daily.provider,
           gateway_usage_daily.upstream_model, gateway_usage_daily.account_id)
)
WHERE (day, caller, model, provider, upstream_model, account_id) IN (
    SELECT day, caller, model, provider, upstream_model, account_id FROM split
);

-- +goose Down
ALTER TABLE gateway_usage_daily DROP COLUMN measured_cached_tokens;
ALTER TABLE gateway_usage_daily DROP COLUMN measured_prompt_tokens;
ALTER TABLE gateway_usage_daily DROP COLUMN estimated_prompt_tokens;
ALTER TABLE gateway_usage_daily DROP COLUMN estimated_requests;
