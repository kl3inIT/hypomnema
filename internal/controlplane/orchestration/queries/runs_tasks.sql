-- name: InsertRun :exec
INSERT INTO runs (id, objective, status, created_by, repo, repo_host, base, coordinator_token_id, chat_id, uses,
                  bot_id, routed_by, created_at, updated_at)
VALUES (@id, @objective, 'open', @created_by, @repo, @repo_host, @base, @coordinator_token_id, @chat_id, @uses,
        @bot, @routed_by, @now, @now);

-- name: GetRun :one
SELECT * FROM runs WHERE id = @id;

-- name: BindRunToChat :exec
UPDATE runs SET chat_id = @chat_id, coordinator_token_id = @coordinator_token_id, bot_id = @bot, updated_at = @now,
    wake_after = coalesce((SELECT max(seq) FROM events WHERE run_id = runs.id), 0)
WHERE id = @id;

-- name: VoidOpenRunWakes :exec
-- The wake-ups of the chat's open Runs that its pi was not given yet go to it no more (issue #1169): the Runs move
-- to the Bot's new main chat (MoveOpenRuns), whose pi reads them, and the former chat's pi may no longer change them.
UPDATE chat_messages SET delivered_at = @now
WHERE chat_messages.chat_id = @chat_id AND chat_messages.delivered_at IS NULL
  AND chat_messages.event_seq IN (SELECT e.seq FROM events e JOIN runs r ON r.id = e.run_id
                                  WHERE r.chat_id = @chat_id AND r.status = 'open');

-- name: MoveOpenRuns :many
-- The open Runs of a Bot's former main chat move to its new main chat (issue #1169), as BindRunToChat binds one:
-- the new chat is woken by their events from now on, and their coordinator token stays.
UPDATE runs SET chat_id = @to_chat, updated_at = @now,
    wake_after = coalesce((SELECT max(seq) FROM events WHERE run_id = runs.id), 0)
WHERE chat_id = @from_chat AND status = 'open'
RETURNING id;

-- name: ChatHasOpenRuns :one
SELECT EXISTS (SELECT 1 FROM runs WHERE chat_id = @chat_id AND status = 'open');

-- name: GetRunWithBot :one
-- A Run with the Bot of its chat; bot is empty for a Run without a chat.
SELECT sqlc.embed(runs), CAST(coalesce(chats.bot_id, '') AS TEXT) AS bot
FROM runs LEFT JOIN chats ON chats.id = runs.chat_id
WHERE runs.id = @id;

-- name: ListRuns :many
-- Newest first, after the cursor (created_at, id) of the previous page when one is given; each with the Bot of its
-- chat, empty for a Run without a chat. Empty filters match everything.
SELECT sqlc.embed(runs), CAST(coalesce(chats.bot_id, '') AS TEXT) AS bot
FROM runs LEFT JOIN chats ON chats.id = runs.chat_id
WHERE (CAST(@status AS TEXT) = '' OR runs.status = @status)
  AND (CAST(@chat_id AS TEXT) = '' OR runs.chat_id = @chat_id)
  AND (CAST(@has_cursor AS INTEGER) = 0 OR runs.created_at < @cursor_at
       OR (runs.created_at = @cursor_at AND runs.id < @cursor_id))
ORDER BY runs.created_at DESC, runs.id DESC
LIMIT @lim;

-- name: TransitionRun :execrows
UPDATE runs
SET status = @to_status, updated_at = @now, finished_at = @now
WHERE id = @id AND status = @from_status;

-- name: NextTaskPosition :one
SELECT CAST(coalesce(max(position), 0) + 1 AS INTEGER) AS next FROM tasks WHERE run_id = @run_id;

-- name: InsertTask :exec
INSERT INTO tasks (id, run_id, position, profile, spec, runtime, model, effort, status, blocked_reason,
                   created_at, updated_at, kind, author_vendor, continues, push_branch, start_from, pr_base)
VALUES (@id, @run_id, @position, @profile, @spec, @runtime, @model, @effort, @status, @blocked_reason,
        @now, @now, @kind, @author_vendor, @continues, @push_branch, @start_from, @pr_base);

-- name: UnfinishedTaskPushingTo :one
-- An unfinished Task, other than the one named, whose publish_branch pushes to branch: its own hyp/<id>, or the
-- branch it continues (issue #690).
SELECT id FROM tasks
WHERE id <> @except
  AND status NOT IN ('succeeded', 'failed', 'canceled')
  AND (push_branch = CAST(@branch AS TEXT) OR (push_branch IS NULL AND 'hyp/' || id = CAST(@branch AS TEXT)))
ORDER BY created_at, id
LIMIT 1;

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

-- name: SetTaskWaitingReason :exec
-- Why a ready Task waits, as the scheduler found (issue #660); NULL clears it. updated_at stays: it is when the Task
-- became ready, which the unplaceable escalation counts from.
UPDATE tasks SET waiting_reason = @waiting_reason WHERE id = @id;

-- name: SetTaskNotBefore :exec
UPDATE tasks SET not_before = @not_before, updated_at = @now WHERE id = @id;

-- name: RequestTaskCancel :exec
UPDATE tasks SET cancel_requested_at = coalesce(cancel_requested_at, @now), updated_at = @now WHERE id = @id;

-- name: ResetTaskFailures :exec
UPDATE tasks SET failure_count = 0, not_before = NULL, updated_at = @now WHERE id = @id;

-- name: InsertFollowUp :exec
INSERT INTO task_follow_ups (id, task_id, note, asked_by, created_at)
VALUES (@id, @task_id, @note, @asked_by, @now);

-- name: ListFollowUpsOfTask :many
-- Oldest first.
SELECT * FROM task_follow_ups WHERE task_id = @task_id ORDER BY created_at, rowid;

-- name: PendingFollowUpOfTask :one
-- The newest note that no Dispatch which gave it to the worker has yet ended succeeded.
SELECT f.* FROM task_follow_ups f
WHERE f.task_id = @task_id
  AND NOT EXISTS (SELECT 1 FROM dispatches d WHERE d.follow_up_id = f.id AND d.status = 'succeeded')
ORDER BY f.created_at DESC, f.rowid DESC
LIMIT 1;

-- name: GetFollowUp :one
SELECT * FROM task_follow_ups WHERE id = @id;

-- name: SucceededTasksOfOpenRuns :many
-- The Tasks whose pull request the control plane may follow after they succeeded (issue #1053).
SELECT t.* FROM tasks t JOIN runs r ON r.id = t.run_id
WHERE r.status = 'open' AND t.status = 'succeeded'
ORDER BY t.run_id, t.position;
