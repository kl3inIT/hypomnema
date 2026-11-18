-- What a Dispatch, a Task and a Run used (issue #856). The model gateway records each request on the key it was
-- sent with, and a Dispatch's worker has keys of its own (gateway_keys.dispatch_id), so a Dispatch's requests are
-- found through its keys. These queries read the gateway's tables and write only dispatch_usage.

-- name: RecordDispatchUsage :exec
-- The Dispatch's totals when it settles, kept after the request rows are purged. Nothing is written for a Dispatch
-- that sent the gateway nothing.
INSERT INTO dispatch_usage (dispatch_id, requests, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens,
                            cache_write_tokens, cost_micros, unpriced, recorded_at)
SELECT k.dispatch_id, COUNT(*), SUM(u.prompt_tokens), SUM(u.completion_tokens), SUM(u.cached_tokens),
       SUM(u.reasoning_tokens), SUM(u.cache_write_tokens), COALESCE(SUM(u.cost_micros), 0),
       SUM(u.cost_micros IS NULL AND u.prompt_tokens + u.completion_tokens + u.web_searches + u.audio_ms + u.characters > 0),
       CAST(@now AS INTEGER)
FROM gateway_keys k JOIN gateway_usage u ON u.key_id = k.id
WHERE k.dispatch_id = CAST(@dispatch_id AS TEXT)
GROUP BY k.dispatch_id
ON CONFLICT (dispatch_id) DO UPDATE SET
    requests = excluded.requests, prompt_tokens = excluded.prompt_tokens,
    completion_tokens = excluded.completion_tokens, cached_tokens = excluded.cached_tokens,
    reasoning_tokens = excluded.reasoning_tokens, cache_write_tokens = excluded.cache_write_tokens,
    cost_micros = excluded.cost_micros, unpriced = excluded.unpriced, recorded_at = excluded.recorded_at;

-- name: ListDispatchUsage :many
-- Each Dispatch of a Task, or of every Task of a Run, with what the gateway's request rows still say of it (live),
-- what was kept when it settled (kept), and the tokens its runtime reported in its result.
SELECT d.id, d.task_id,
       CAST(COALESCE(l.requests, 0) AS INTEGER) AS live_requests,
       CAST(COALESCE(l.prompt_tokens, 0) AS INTEGER) AS live_prompt_tokens,
       CAST(COALESCE(l.completion_tokens, 0) AS INTEGER) AS live_completion_tokens,
       CAST(COALESCE(l.cached_tokens, 0) AS INTEGER) AS live_cached_tokens,
       CAST(COALESCE(l.reasoning_tokens, 0) AS INTEGER) AS live_reasoning_tokens,
       CAST(COALESCE(l.cache_write_tokens, 0) AS INTEGER) AS live_cache_write_tokens,
       CAST(COALESCE(l.cost_micros, 0) AS INTEGER) AS live_cost_micros,
       CAST(COALESCE(l.unpriced, 0) AS INTEGER) AS live_unpriced,
       CAST(COALESCE(k.requests, 0) AS INTEGER) AS kept_requests,
       CAST(COALESCE(k.prompt_tokens, 0) AS INTEGER) AS kept_prompt_tokens,
       CAST(COALESCE(k.completion_tokens, 0) AS INTEGER) AS kept_completion_tokens,
       CAST(COALESCE(k.cached_tokens, 0) AS INTEGER) AS kept_cached_tokens,
       CAST(COALESCE(k.reasoning_tokens, 0) AS INTEGER) AS kept_reasoning_tokens,
       CAST(COALESCE(k.cache_write_tokens, 0) AS INTEGER) AS kept_cache_write_tokens,
       CAST(COALESCE(k.cost_micros, 0) AS INTEGER) AS kept_cost_micros,
       CAST(COALESCE(k.unpriced, 0) AS INTEGER) AS kept_unpriced,
       CAST(COALESCE(json_extract(d.result, '$.usage.input'), 0) AS INTEGER) AS reported_input,
       CAST(COALESCE(json_extract(d.result, '$.usage.output'), 0) AS INTEGER) AS reported_output,
       CAST(COALESCE(json_extract(d.result, '$.usage.cache_read'), 0) AS INTEGER) AS reported_cache_read,
       CAST(COALESCE(json_extract(d.result, '$.usage.cache_write'), 0) AS INTEGER) AS reported_cache_write
FROM dispatches d
JOIN tasks t ON t.id = d.task_id
LEFT JOIN dispatch_usage k ON k.dispatch_id = d.id
LEFT JOIN (
    SELECT gk.dispatch_id, COUNT(*) AS requests, SUM(u.prompt_tokens) AS prompt_tokens,
           SUM(u.completion_tokens) AS completion_tokens, SUM(u.cached_tokens) AS cached_tokens,
           SUM(u.reasoning_tokens) AS reasoning_tokens, SUM(u.cache_write_tokens) AS cache_write_tokens,
           COALESCE(SUM(u.cost_micros), 0) AS cost_micros,
           SUM(u.cost_micros IS NULL AND u.prompt_tokens + u.completion_tokens + u.web_searches + u.audio_ms + u.characters > 0) AS unpriced
    FROM gateway_keys gk JOIN gateway_usage u ON u.key_id = gk.id
    WHERE gk.dispatch_id IN (SELECT d2.id FROM dispatches d2 JOIN tasks t2 ON t2.id = d2.task_id
                             WHERE t2.id = @task_id OR t2.run_id = @run_id)
    GROUP BY gk.dispatch_id
) l ON l.dispatch_id = d.id
WHERE t.id = @task_id OR t.run_id = @run_id
ORDER BY d.created_at, d.id;

-- name: ListFailedDispatchesOfTask :many
-- The Task's failed Dispatches, newest first, each with its failure class and the cause it was unknown for, if it
-- was: the cause is cleared from the Dispatch when it settles, and kept in its status event.
SELECT d.id, CAST(d.failure_class AS TEXT) AS failure_class,
       CAST(COALESCE((SELECT json_extract(e.payload, '$.cause') FROM events e
                      WHERE e.task_id = d.task_id AND e.dispatch_id = d.id AND e.kind = 'dispatch.status'
                        AND json_extract(e.payload, '$.cause') IS NOT NULL
                      ORDER BY e.seq DESC LIMIT 1), '') AS TEXT) AS unknown_cause
FROM dispatches d
WHERE d.task_id = @task_id AND d.status = 'failed'
ORDER BY d.settled_at DESC, d.rowid DESC
LIMIT @lim;
