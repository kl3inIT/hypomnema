-- name: InsertQuestion :exec
INSERT INTO questions (id, dispatch_id, task_id, run_id, mode, addressed_to, text, options, status, remind_at,
                       created_at, updated_at)
VALUES (@id, @dispatch_id, @task_id, @run_id, @mode, @addressed_to, @text, @options, 'open', @remind_at, @now, @now);

-- name: GetQuestion :one
SELECT * FROM questions WHERE id = @id;

-- name: OpenWaitQuestionOfDispatch :one
SELECT * FROM questions WHERE dispatch_id = @dispatch_id AND mode = 'wait' AND status = 'open';

-- name: GateOfDispatch :one
SELECT * FROM questions WHERE dispatch_id = @dispatch_id AND mode = 'gate';

-- name: AnswerQuestion :execrows
UPDATE questions
SET updated_at = @now, status = 'answered', answer = @answer, answered_by = @answered_by, answered_at = @now,
    remind_at = NULL
WHERE id = @id AND status = 'open';

-- name: ForwardQuestion :execrows
UPDATE questions SET updated_at = @now, addressed_to = 'owner', remind_at = @remind_at
WHERE id = @id AND status = 'open' AND addressed_to = 'coordinator';

-- name: CancelOpenWaitQuestionsOfDispatch :many
UPDATE questions SET updated_at = @now, status = 'canceled', cancel_reason = 'dispatch_ended', remind_at = NULL
WHERE dispatch_id = @dispatch_id AND mode = 'wait' AND status = 'open'
RETURNING id, task_id, run_id, dispatch_id, mode, addressed_to;

-- name: CancelOpenGatesOfTask :many
UPDATE questions SET updated_at = @now, status = 'canceled', cancel_reason = 'task_canceled', remind_at = NULL
WHERE task_id = @task_id AND mode = 'gate' AND status = 'open'
RETURNING id, task_id, run_id, dispatch_id, mode, addressed_to;

-- name: ListUndeliveredAnswers :many
SELECT * FROM questions
WHERE dispatch_id = @dispatch_id AND mode = 'wait' AND status = 'answered' AND delivered_at IS NULL
ORDER BY created_at;

-- name: MarkDelivered :execrows
UPDATE questions SET updated_at = @now, delivered_at = @now
WHERE id = @id AND dispatch_id = @dispatch_id AND mode = 'wait' AND status = 'answered' AND delivered_at IS NULL;

-- name: ListQuestions :many
-- Empty filters match everything.
SELECT * FROM questions
WHERE (CAST(@run_id AS TEXT) = '' OR run_id = @run_id)
  AND (CAST(@task_id AS TEXT) = '' OR task_id = @task_id)
  AND (CAST(@dispatch_id AS TEXT) = '' OR dispatch_id = @dispatch_id)
  AND (CAST(@addressed_to AS TEXT) = '' OR addressed_to = @addressed_to)
  AND (CAST(@status AS TEXT) = '' OR status = @status)
ORDER BY created_at, id;

-- name: ListDueReminders :many
SELECT id, dispatch_id, task_id, run_id FROM questions
WHERE status = 'open' AND addressed_to = 'owner' AND remind_at <= CAST(@now AS INTEGER)
ORDER BY remind_at;

-- name: SetRemindAt :exec
UPDATE questions SET updated_at = @now, remind_at = @remind_at WHERE id = @id;
