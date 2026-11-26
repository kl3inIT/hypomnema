-- name: InsertEvent :exec
INSERT INTO events (kind, run_id, task_id, dispatch_id, question_id, node_id, payload, created_at)
VALUES (@kind, @run_id, @task_id, @dispatch_id, @question_id, @node_id, @payload, @now);

-- name: ListEvents :many
-- Empty filters match everything.
SELECT * FROM events
WHERE seq > @after
  AND (CAST(@run_id AS TEXT) = '' OR run_id = @run_id)
  AND (CAST(@task_id AS TEXT) = '' OR task_id = @task_id)
ORDER BY seq
LIMIT @lim;

-- name: LastEventSeq :one
SELECT CAST(coalesce(max(seq), 0) AS INTEGER) AS seq FROM events;

-- name: LatestRunTaskEvent :one
SELECT CAST(coalesce((
    SELECT e.payload FROM events e JOIN runs r ON r.id = e.run_id
    WHERE e.run_id = @run_id AND r.status = 'open' AND e.kind IN ('task.created', 'task.status')
    ORDER BY e.seq DESC LIMIT 1
), '{}') AS TEXT) AS payload;

-- name: PullRequestEscalationOf :one
-- Whether the pull request a Dispatch reported was already settled (issue #1053): its escalation of kind
-- pull_request.
SELECT CAST(count(*) AS INTEGER) AS n FROM events
WHERE task_id = @task_id AND dispatch_id = @dispatch_id AND kind = 'escalation' AND json_extract(payload, '$.kind') = 'pull_request';

-- name: PullRequestLookupOf :one
-- The pull request found on a Dispatch's Task's branch, and when it was first looked up (issue #1168).
SELECT * FROM pull_request_lookups WHERE dispatch_id = @dispatch_id;

-- name: NotePullRequestLookup :exec
-- Keeps what a lookup found: since stays the first lookup's time, and a found pull request is not forgotten by a
-- later lookup that found none (issue #1168).
INSERT INTO pull_request_lookups (dispatch_id, url, since) VALUES (@dispatch_id, @url, @now)
ON CONFLICT (dispatch_id) DO UPDATE SET url = excluded.url WHERE excluded.url <> '';
