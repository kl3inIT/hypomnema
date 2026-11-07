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
