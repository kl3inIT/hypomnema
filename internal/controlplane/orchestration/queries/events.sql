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
