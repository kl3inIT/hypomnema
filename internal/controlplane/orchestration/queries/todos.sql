-- name: InsertTodo :exec
INSERT INTO todos (id, title, notes, done_when, due_kind, due_date, due_zone, due_at, overdue_at, assignee_kind,
                   assignee, state, source_kind, source_id, source_account, source_quote, source_ref,
                   created_by_kind, created_by, idempotency_key, idempotency_hash, created_at, updated_at)
VALUES (@id, @title, @notes, @done_when, @due_kind, @due_date, @due_zone, @due_at, @overdue_at, @assignee_kind,
        @assignee, @state, @source_kind, @source_id, @source_account, @source_quote, @source_ref, @created_by_kind,
        @created_by, @idempotency_key, @idempotency_hash, @now, @now);

-- name: GetTodo :one
SELECT * FROM todos WHERE id = @id;

-- name: GetTodoByIdempotencyKey :one
SELECT * FROM todos WHERE idempotency_key = @idempotency_key;

-- name: UpdateTodo :execrows
-- The guarded write of every change (docs/conventions.md, "Transitions are guarded writes"): it changes the row only
-- at the revision and in the state the caller read, and moves the revision on by one.
UPDATE todos
SET revision = revision + 1, title = @title, notes = @notes, done_when = @done_when, due_kind = @due_kind,
    due_date = @due_date, due_zone = @due_zone, due_at = @due_at, overdue_at = @overdue_at,
    assignee_kind = @assignee_kind, assignee = @assignee, state = @state, snoozed_until = @snoozed_until,
    work_attempt = @work_attempt, work_kind = @work_kind, work_id = @work_id, work_bot = @work_bot,
    work_started_at = @work_started_at,
    approval_id = @approval_id, done_by_kind = @done_by_kind,
    done_by = @done_by, recorded_by_kind = @recorded_by_kind, recorded_by = @recorded_by, closed_how = @closed_how,
    close_reason = @close_reason, evidence = @evidence, closed_at = @closed_at, duplicate_of = @duplicate_of,
    updated_at = @now
WHERE id = @id AND revision = @revision AND state = @from_state;

-- name: GetTodoChatMessage :one
-- Who wrote a chat message and in which chat: the owner's message cited as evidence (role owner, no sender).
SELECT chat_id, role, sender_id FROM chat_messages WHERE seq = @seq;

-- name: CountDuplicatesOf :one
-- The items marked duplicate of this one.
SELECT count(*) FROM todos WHERE duplicate_of = @id AND state = 'duplicate';

-- name: InsertTodoEvent :exec
INSERT INTO todo_events (todo_id, at, revision, actor_kind, actor, kind, before, after)
VALUES (@todo_id, @at, @revision, @actor_kind, @actor, @kind, @before, @after);

-- name: ListTodoEvents :many
-- The latest events of an item, newest first, at most lim.
SELECT * FROM todo_events WHERE todo_id = @todo_id ORDER BY id DESC LIMIT @lim;

-- name: ListTodos :many
-- One page of the items that match, by due (those without one last), then created, then id, after the cursor of
-- the previous page when one is given. The filter is the same as CountTodos's.
SELECT t.* FROM todos t
WHERE (CAST(@any_state AS INTEGER) = 1 OR t.state IN (SELECT value FROM json_each(CAST(@states AS TEXT))))
  AND (CAST(@assignee_kind AS TEXT) = '' OR (t.assignee_kind = @assignee_kind AND t.assignee = @assignee))
  AND (CAST(@source_kind AS TEXT) = '' OR t.source_kind = @source_kind)
  AND (CAST(@has_due_from AS INTEGER) = 0 OR t.due_at >= CAST(@due_from AS INTEGER))
  AND (CAST(@has_due_to AS INTEGER) = 0 OR t.due_at < CAST(@due_to AS INTEGER))
  AND (CAST(@with_snoozed AS INTEGER) = 1 OR t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
  AND (CAST(@needs_you AS INTEGER) = 0 OR (
        t.state NOT IN ('done', 'dropped', 'duplicate')
    AND (t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
    AND (t.state = 'proposed'
      OR (t.state = 'review' AND EXISTS (SELECT 1 FROM approvals a WHERE a.id = t.approval_id AND a.status = 'pending'))
      OR (t.assignee_kind = 'me' AND t.overdue_at IS NOT NULL AND t.overdue_at <= CAST(@now AS INTEGER)))))
  AND (CAST(@has_cursor AS INTEGER) = 0
    OR coalesce(t.due_at, 9223372036854775807) > CAST(@cursor_due AS INTEGER)
    OR (coalesce(t.due_at, 9223372036854775807) = CAST(@cursor_due AS INTEGER) AND t.created_at > @cursor_at)
    OR (coalesce(t.due_at, 9223372036854775807) = CAST(@cursor_due AS INTEGER) AND t.created_at = @cursor_at AND t.id > @cursor_id))
ORDER BY coalesce(t.due_at, 9223372036854775807), t.created_at, t.id
LIMIT @lim;

-- name: CountTodos :one
-- How many items match the filter of ListTodos, every page together.
SELECT count(*) FROM todos t
WHERE (CAST(@any_state AS INTEGER) = 1 OR t.state IN (SELECT value FROM json_each(CAST(@states AS TEXT))))
  AND (CAST(@assignee_kind AS TEXT) = '' OR (t.assignee_kind = @assignee_kind AND t.assignee = @assignee))
  AND (CAST(@source_kind AS TEXT) = '' OR t.source_kind = @source_kind)
  AND (CAST(@has_due_from AS INTEGER) = 0 OR t.due_at >= CAST(@due_from AS INTEGER))
  AND (CAST(@has_due_to AS INTEGER) = 0 OR t.due_at < CAST(@due_to AS INTEGER))
  AND (CAST(@with_snoozed AS INTEGER) = 1 OR t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
  AND (CAST(@needs_you AS INTEGER) = 0 OR (
        t.state NOT IN ('done', 'dropped', 'duplicate')
    AND (t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
    AND (t.state = 'proposed'
      OR (t.state = 'review' AND EXISTS (SELECT 1 FROM approvals a WHERE a.id = t.approval_id AND a.status = 'pending'))
      OR (t.assignee_kind = 'me' AND t.overdue_at IS NOT NULL AND t.overdue_at <= CAST(@now AS INTEGER)))));
