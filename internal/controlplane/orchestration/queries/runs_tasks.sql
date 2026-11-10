-- name: InsertRun :exec
INSERT INTO runs (id, objective, status, created_by, repo, repo_host, base, coordinator_token_id, chat_id,
                  created_at, updated_at)
VALUES (@id, @objective, 'open', @created_by, @repo, @repo_host, @base, @coordinator_token_id, @chat_id, @now, @now);

-- name: GetRun :one
SELECT * FROM runs WHERE id = @id;

-- name: ListRuns :many
-- Newest first, after the cursor (created_at, id) of the previous page when one is given.
SELECT * FROM runs
WHERE (CAST(@status AS TEXT) = '' OR status = @status)
  AND (CAST(@has_cursor AS INTEGER) = 0 OR created_at < @cursor_at OR (created_at = @cursor_at AND id < @cursor_id))
ORDER BY created_at DESC, id DESC
LIMIT @lim;

-- name: TransitionRun :execrows
UPDATE runs
SET status = @to_status, updated_at = @now, finished_at = @now
WHERE id = @id AND status = @from_status;

-- name: NextTaskPosition :one
SELECT CAST(coalesce(max(position), 0) + 1 AS INTEGER) AS next FROM tasks WHERE run_id = @run_id;

-- name: InsertTask :exec
INSERT INTO tasks (id, run_id, position, profile, spec, runtime, model, effort, status, blocked_reason,
                   created_at, updated_at)
VALUES (@id, @run_id, @position, @profile, @spec, @runtime, @model, @effort, @status, @blocked_reason, @now, @now);

-- name: InsertTaskDep :exec
INSERT INTO task_deps (task_id, depends_on) VALUES (@task_id, @depends_on);

-- name: GetTask :one
SELECT * FROM tasks WHERE id = @id;

-- name: ListTasksByRun :many
SELECT * FROM tasks WHERE run_id = @run_id ORDER BY position;

-- name: ListBoardTasks :many
-- Every Task of an open Run, and the other Runs' Tasks changed at or after since; most recently changed first.
SELECT * FROM tasks
WHERE tasks.run_id IN (SELECT r.id FROM runs r WHERE r.status = 'open') OR tasks.updated_at >= @since
ORDER BY tasks.updated_at DESC, tasks.id DESC
LIMIT @lim;

-- name: TransitionTask :execrows
UPDATE tasks
SET status         = @to_status,
    blocked_reason = @blocked_reason,
    result         = coalesce(@result, result),
    updated_at     = @now,
    finished_at    = @finished_at
WHERE id = @id AND status IN (sqlc.slice('from_statuses'));

-- name: ListDependents :many
SELECT t.id, t.status
FROM task_deps d
JOIN tasks t ON t.id = d.task_id
WHERE d.depends_on = @task_id
ORDER BY t.position;

-- name: ListUnblockedDependents :many
-- Pending dependents of a Task whose every dependency has succeeded.
SELECT t.id
FROM task_deps d
JOIN tasks t ON t.id = d.task_id
WHERE d.depends_on = @task_id
  AND t.status = 'pending'
  AND NOT EXISTS (
      SELECT 1 FROM task_deps d2 JOIN tasks u ON u.id = d2.depends_on
      WHERE d2.task_id = t.id AND u.status <> 'succeeded')
ORDER BY t.position;

-- name: CountUnfinishedTasks :one
SELECT count(*) FROM tasks WHERE run_id = @run_id AND status IN (sqlc.slice('statuses'));

-- name: ListDependencyStates :many
SELECT u.status, u.blocked_reason
FROM task_deps d
JOIN tasks u ON u.id = d.depends_on
WHERE d.task_id = @task_id;

-- name: ListUnfinishedTaskIDs :many
SELECT id FROM tasks WHERE run_id = @run_id AND status IN (sqlc.slice('statuses')) ORDER BY position;

-- name: RecordTaskFailure :one
UPDATE tasks SET failure_count = failure_count + 1, updated_at = @now WHERE id = @id
RETURNING failure_count;

-- name: SetTaskNotBefore :exec
UPDATE tasks SET not_before = @not_before, updated_at = @now WHERE id = @id;

-- name: RequestTaskCancel :exec
UPDATE tasks SET cancel_requested_at = coalesce(cancel_requested_at, @now), updated_at = @now WHERE id = @id;

-- name: ResetTaskFailures :exec
UPDATE tasks SET failure_count = 0, not_before = NULL, updated_at = @now WHERE id = @id;
