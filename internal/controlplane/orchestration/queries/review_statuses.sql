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

-- name: KeepNoReviewRequest :exec
-- A Task that succeeds again (after a follow-up) is looked up again from now.
INSERT INTO no_review_requests (task_id, repo_host, repo, branch, pull_request, since)
VALUES (@task_id, @repo_host, @repo, @branch, @pull_request, @now)
ON CONFLICT (task_id) DO UPDATE SET repo_host    = excluded.repo_host,
                                    repo         = excluded.repo,
                                    branch       = excluded.branch,
                                    pull_request = CASE WHEN excluded.pull_request = ''
                                                        THEN no_review_requests.pull_request
                                                        ELSE excluded.pull_request END,
                                    since        = excluded.since,
                                    done_at      = NULL;

-- name: OpenNoReviewRequests :many
SELECT * FROM no_review_requests
WHERE done_at IS NULL
ORDER BY since, task_id;

-- name: NoteNoReviewRequest :exec
UPDATE no_review_requests
SET pull_request = @pull_request
WHERE task_id = @task_id;

-- name: EndNoReviewRequest :exec
-- Only the request as it was read: a Task that succeeded again meanwhile is looked up again.
UPDATE no_review_requests
SET done_at = @now
WHERE task_id = @task_id
  AND since = @since
  AND done_at IS NULL;

-- name: LookedUpNoReviewRequest :exec
UPDATE no_review_requests
SET looked_at = @now
WHERE task_id = @task_id;

-- name: TaskThatPushed :one
-- The Task whose Dispatch reported publishing sha to branch (published_heads.go), the latest report first.
SELECT t.* FROM tasks t
    JOIN dispatches d ON d.task_id = t.id
    JOIN dispatch_effects e ON e.dispatch_id = d.id
WHERE e.kind = 'git_push' AND e.phase = 'done' AND e.ref = CAST(@branch AS TEXT) AND e.payload = CAST(@sha AS TEXT)
ORDER BY e.updated_at DESC, d.id DESC
LIMIT 1;
