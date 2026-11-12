-- name: InsertApproval :exec
INSERT INTO approvals (id, integration, label, action, action_name, dispatch_id, task_id, run_id, chat_id, asked_by,
                       title, payload, match_hash, routine_id, routine_run_id, created_at)
VALUES (@id, @integration, @label, @action, @action_name, @dispatch_id, @task_id, @run_id, @chat_id, @asked_by,
        @title, @payload, @match_hash, @routine_id, @routine_run_id, @now);

-- name: GetApproval :one
SELECT * FROM approvals WHERE id = @id;

-- name: ListApprovals :many
-- Empty filters match everything. The newest first.
SELECT * FROM approvals
WHERE (CAST(@status AS TEXT) = '' OR status = @status)
  AND (CAST(@chat_id AS TEXT) = '' OR chat_id = @chat_id)
  AND (CAST(@task_id AS TEXT) = '' OR task_id = @task_id)
  AND (CAST(@routine_id AS TEXT) = '' OR routine_id = @routine_id)
ORDER BY created_at DESC, id DESC
LIMIT @max_rows;

-- name: ListApprovalsByMatch :many
-- The Approvals of one decoded request, the newest first: the caller picks the one of its own Run or chat.
SELECT * FROM approvals
WHERE match_hash = @match_hash AND integration = @integration AND label = @label AND action = @action
ORDER BY created_at DESC, id DESC;

-- name: CountSessionApprovals :one
-- Whether the owner approved the action for the rest of this Run or this chat.
SELECT COUNT(*) FROM approvals
WHERE status = 'approved' AND scope = 'session'
  AND integration = @integration AND label = @label AND action = @action
  AND ((CAST(@run_id AS TEXT) <> '' AND run_id = @run_id) OR (CAST(@chat_id AS TEXT) <> '' AND chat_id = @chat_id));

-- name: DecideApproval :execrows
UPDATE approvals SET status = @status, scope = @scope, standing = @standing, decided_at = @now
WHERE id = @id AND status = 'pending';

-- name: UseApproval :execrows
-- An approval for one request runs once.
UPDATE approvals SET used_at = @now WHERE id = @id AND status = 'approved' AND scope = 'once' AND used_at IS NULL;

-- name: ListActionPolicies :many
SELECT * FROM action_policies WHERE integration = @integration AND label = @label ORDER BY action;

-- name: UpsertActionPolicy :exec
INSERT INTO action_policies (integration, label, action, policy, updated_at)
VALUES (@integration, @label, @action, @policy, @now)
ON CONFLICT (integration, label, action) DO UPDATE SET policy = excluded.policy, updated_at = excluded.updated_at;

-- name: DeleteActionPolicy :exec
DELETE FROM action_policies WHERE integration = @integration AND label = @label AND action = @action;

-- name: DeleteActionPoliciesOfConnection :exec
DELETE FROM action_policies WHERE integration = @integration AND label = @label;

-- name: OpenQuestionOfDispatch :one
-- The question a Dispatch's worker waits on; a worker waits on one at a time.
SELECT id FROM questions WHERE dispatch_id = @dispatch_id AND status = 'open' AND addressed_to = 'owner'
ORDER BY created_at DESC LIMIT 1;

-- name: CountApprovedOfTask :one
-- The requests to a service the owner approved for a Task: what its worker did outside the worktree.
SELECT COUNT(*) FROM approvals WHERE task_id = @task_id AND status = 'approved';
