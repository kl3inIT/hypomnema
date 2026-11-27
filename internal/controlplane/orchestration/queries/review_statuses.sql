-- name: QueueReviewVerdictStatus :exec
-- A verdict's status replaces what its head had: a later verdict on the same head is the one posted.
INSERT INTO review_statuses (pull_request, head_sha, task_id, reason, queued_at)
VALUES (@pull_request, @head_sha, @task_id, @reason, @now)
ON CONFLICT (pull_request, head_sha) DO UPDATE SET task_id   = excluded.task_id,
                                                   reason    = excluded.reason,
                                                   queued_at = excluded.queued_at,
                                                   done_at   = NULL,
                                                   outcome   = NULL;

-- name: QueueNoReviewStatus :exec
-- A head that needs no review gets its status once; a verdict on it wins.
INSERT INTO review_statuses (pull_request, head_sha, task_id, reason, queued_at)
VALUES (@pull_request, @head_sha, @task_id, 'quick', @now)
ON CONFLICT (pull_request, head_sha) DO NOTHING;

-- name: WaitingReviewStatuses :many
SELECT * FROM review_statuses
WHERE done_at IS NULL
ORDER BY queued_at, pull_request, head_sha;

-- name: FinishReviewStatus :exec
-- Only the row as it was read: a verdict queued meanwhile waits to be posted.
UPDATE review_statuses
SET done_at = @now,
    outcome = @outcome
WHERE pull_request = @pull_request
  AND head_sha = @head_sha
  AND task_id = @task_id
  AND queued_at = @queued_at
  AND done_at IS NULL;
