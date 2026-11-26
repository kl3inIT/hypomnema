-- name: InsertTodo :exec
INSERT INTO todos (id, title, notes, done_when, draft, due_kind, due_date, due_zone, due_at, overdue_at, assignee_kind,
                   assignee, passed_by, state, source_kind, source_id, source_account, source_quote, source_ref,
                   created_by_kind, created_by, why_open, priority, priority_reason, project_id, idempotency_key,
                   idempotency_hash, created_at, updated_at)
VALUES (@id, @title, @notes, @done_when, @draft, @due_kind, @due_date, @due_zone, @due_at, @overdue_at, @assignee_kind,
        @assignee, @passed_by, @state, @source_kind, @source_id, @source_account, @source_quote, @source_ref, @created_by_kind,
        @created_by, @why_open, @priority, @priority_reason, @project_id, @idempotency_key, @idempotency_hash, @now, @now);

-- name: GetTodo :one
SELECT * FROM todos WHERE id = @id;

-- name: GetTodoByIdempotencyKey :one
SELECT * FROM todos WHERE idempotency_key = @idempotency_key;

-- name: UpdateTodo :execrows
-- The guarded write of every change (docs/conventions.md, "Transitions are guarded writes"): it changes the row only
-- at the revision and in the state the caller read, and moves the revision on by one.
UPDATE todos
SET revision = revision + 1, title = @title, notes = @notes, done_when = @done_when, draft = @draft,
    due_kind = @due_kind,
    due_date = @due_date, due_zone = @due_zone, due_at = @due_at, overdue_at = @overdue_at,
    assignee_kind = @assignee_kind, assignee = @assignee, passed_by = @passed_by, state = @state,
    snoozed_until = @snoozed_until,
    source_kind = @source_kind, source_id = @source_id, source_account = @source_account,
    source_quote = @source_quote, source_ref = @source_ref,
    work_attempt = @work_attempt, work_kind = @work_kind, work_id = @work_id, work_bot = @work_bot,
    work_started_at = @work_started_at,
    approval_id = @approval_id, done_by_kind = @done_by_kind,
    done_by = @done_by, recorded_by_kind = @recorded_by_kind, recorded_by = @recorded_by, closed_how = @closed_how,
    close_reason = @close_reason, evidence = @evidence, closed_at = @closed_at, duplicate_of = @duplicate_of,
    priority = @priority, priority_reason = @priority_reason, project_id = @project_id, why_open = @why_open,
    updated_at = @now
WHERE id = @id AND revision = @revision AND state = @from_state;

-- name: GetTodoOfApproval :one
-- The item that waits for an Approval.
SELECT * FROM todos WHERE approval_id = @approval_id AND state = 'review';

-- name: GetTodoOfAttempt :one
-- The item a Bot's attempt is on.
SELECT * FROM todos WHERE work_attempt = @work_attempt AND state IN ('doing', 'review');

-- name: ListTodosOfWork :many
-- The items a Bot's attempt is on in a chat or a Routine's run, doing or waiting for an Approval.
SELECT * FROM todos WHERE work_kind = @work_kind AND work_id = @work_id AND state IN ('doing', 'review') ORDER BY id;

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

-- name: ListOwnerTodoEvents :many
-- The latest changes the owner made to an item at or after since, newest first, at most lim (issue #1119).
SELECT * FROM todo_events WHERE todo_id = @todo_id AND actor_kind = 'owner' AND at >= @since
ORDER BY id DESC LIMIT @lim;

-- name: ListTodos :many
-- One page of the items that match, by due (those without one last), then created, then id, after the cursor of
-- the previous page when one is given. The filter is the same as CountTodos's. With project_kind none, only the items
-- without a project; one, only those of project (issue #1157). A proposal nobody touched since
-- stale_before is stale (issue #966): left out unless with_stale, and never one that needs the owner. Touched is
-- any event after created, and every event moves the revision on, so an untouched item is at revision 1. With
-- has_owner_since, only the items the owner changed at or after owner_since (issue #1119).
SELECT t.* FROM todos t
WHERE (CAST(@any_state AS INTEGER) = 1 OR t.state IN (SELECT value FROM json_each(CAST(@states AS TEXT))))
  AND (CAST(@assignee_kind AS TEXT) = '' OR (t.assignee_kind = @assignee_kind AND t.assignee = @assignee))
  AND (CAST(@source_kind AS TEXT) = '' OR t.source_kind = @source_kind)
  AND (CAST(@project_kind AS TEXT) = ''
    OR (CAST(@project_kind AS TEXT) = 'none' AND t.project_id IS NULL)
    OR (CAST(@project_kind AS TEXT) = 'one' AND t.project_id = CAST(@project AS TEXT)))
  AND (CAST(@has_due_from AS INTEGER) = 0 OR t.due_at >= CAST(@due_from AS INTEGER))
  AND (CAST(@has_due_to AS INTEGER) = 0 OR t.due_at < CAST(@due_to AS INTEGER))
  AND (CAST(@with_snoozed AS INTEGER) = 1 OR t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
  AND (CAST(@with_stale AS INTEGER) = 1
    OR NOT (t.state = 'proposed' AND t.revision = 1 AND t.created_at <= CAST(@stale_before AS INTEGER)))
  AND (CAST(@needs_you AS INTEGER) = 0 OR (
        t.state NOT IN ('done', 'dropped', 'duplicate')
    AND NOT (t.state = 'proposed' AND t.revision = 1 AND t.created_at <= CAST(@stale_before AS INTEGER))
    AND (t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
    AND (t.state = 'proposed'
      OR (t.state = 'review' AND EXISTS (SELECT 1 FROM approvals a WHERE a.id = t.approval_id AND a.status = 'pending'))
      OR (t.assignee_kind = 'me' AND t.draft <> '')
      OR (t.assignee_kind = 'me' AND t.idempotency_hash = 'signin')
      OR (t.assignee_kind = 'me' AND t.overdue_at IS NOT NULL AND t.overdue_at <= CAST(@now AS INTEGER)))))
  AND (CAST(@has_owner_since AS INTEGER) = 0 OR EXISTS (SELECT 1 FROM todo_events e
        WHERE e.todo_id = t.id AND e.actor_kind = 'owner' AND e.at >= CAST(@owner_since AS INTEGER)))
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
  AND (CAST(@project_kind AS TEXT) = ''
    OR (CAST(@project_kind AS TEXT) = 'none' AND t.project_id IS NULL)
    OR (CAST(@project_kind AS TEXT) = 'one' AND t.project_id = CAST(@project AS TEXT)))
  AND (CAST(@has_due_from AS INTEGER) = 0 OR t.due_at >= CAST(@due_from AS INTEGER))
  AND (CAST(@has_due_to AS INTEGER) = 0 OR t.due_at < CAST(@due_to AS INTEGER))
  AND (CAST(@with_snoozed AS INTEGER) = 1 OR t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
  AND (CAST(@with_stale AS INTEGER) = 1
    OR NOT (t.state = 'proposed' AND t.revision = 1 AND t.created_at <= CAST(@stale_before AS INTEGER)))
  AND (CAST(@needs_you AS INTEGER) = 0 OR (
        t.state NOT IN ('done', 'dropped', 'duplicate')
    AND NOT (t.state = 'proposed' AND t.revision = 1 AND t.created_at <= CAST(@stale_before AS INTEGER))
    AND (t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
    AND (t.state = 'proposed'
      OR (t.state = 'review' AND EXISTS (SELECT 1 FROM approvals a WHERE a.id = t.approval_id AND a.status = 'pending'))
      OR (t.assignee_kind = 'me' AND t.draft <> '')
      OR (t.assignee_kind = 'me' AND t.idempotency_hash = 'signin')
      OR (t.assignee_kind = 'me' AND t.overdue_at IS NOT NULL AND t.overdue_at <= CAST(@now AS INTEGER)))))
  AND (CAST(@has_owner_since AS INTEGER) = 0 OR EXISTS (SELECT 1 FROM todo_events e
        WHERE e.todo_id = t.id AND e.actor_kind = 'owner' AND e.at >= CAST(@owner_since AS INTEGER)));

-- name: CountOwnerAsks :one
-- What else waits on the owner in the Inbox's Needs you (issue #1093): the pending Approvals and the open questions
-- workers asked him. An Approval that a to-do in review waits for is counted once, as that to-do, unless the to-do
-- is snoozed.
SELECT CAST(
  (SELECT count(*) FROM approvals a
    WHERE a.status = 'pending'
      AND NOT EXISTS (SELECT 1 FROM todos t WHERE t.approval_id = a.id AND t.state = 'review'
        AND (t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))))
  + (SELECT count(*) FROM questions q WHERE q.status = 'open' AND q.addressed_to = 'owner')
AS INTEGER) AS asks;

-- name: SetTodoIdempotencyKey :exec
-- An account's "log in again" item lets the account's key go when it closes, for one of its own, and takes it again
-- when it is reopened (issue #1023), so that the account has one open item.
UPDATE todos SET idempotency_key = @idempotency_key WHERE id = @id;

-- name: TouchTodo :execrows
-- A comment on an item (issue #1111) moves its revision on, as every change does, without changing a field: the
-- item read before it is stale, and a proposal commented on is touched.
UPDATE todos SET revision = revision + 1, updated_at = @now WHERE id = @id AND revision = @revision;

-- name: SetTodoChat :exec
-- The item's own chat with the Bot it is given to (issue #1111).
UPDATE todos SET chat_id = @chat_id WHERE id = @id;

-- name: ClearTodoChats :exec
-- A chat deleted is no item's own chat any more.
UPDATE todos SET chat_id = NULL WHERE chat_id = @chat_id;

-- name: GetTodoChatBot :one
-- The Bot of a chat, read for an item's own chat: none when the chat was deleted.
SELECT bot_id FROM chats WHERE id = @id;

-- name: InsertTodoWake :exec
-- A wake-up of the control plane in an item's own chat (issue #1111): the item handed to the Bot, or the owner's
-- comment on it, given to the Bot as a Routine's prompt is (InsertRoutinePrompt), since nobody wrote it there.
INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
VALUES (@chat_id, 'system', 'wake', @text, 1, @now);

-- name: ListOpenTodosOfBot :many
-- The items not closed a Bot added or was given (issue #1124): those the main Bot takes over when it is removed.
SELECT * FROM todos
WHERE state NOT IN ('done', 'dropped', 'duplicate')
  AND ((created_by_kind = 'bot' AND created_by = @bot) OR (assignee_kind = 'bot' AND assignee = @bot))
ORDER BY id;

-- name: TakeOverTodo :execrows
-- A removed Bot's item becomes the main Bot's (issue #1124): who it counts as added by, who it is given to and
-- through whom, at the revision read, which it moves on.
UPDATE todos
SET revision = revision + 1, created_by = @created_by, assignee = @assignee, passed_by = @passed_by, updated_at = @now
WHERE id = @id AND revision = @revision;

-- name: ListInboxProjects :many
-- The Inbox's projects (issue #1157), by name.
SELECT * FROM inbox_projects ORDER BY name COLLATE NOCASE, id;

-- name: GetInboxProject :one
SELECT * FROM inbox_projects WHERE id = @id;

-- name: InsertInboxProject :exec
INSERT INTO inbox_projects (id, name, color, created_at, updated_at) VALUES (@id, @name, @color, @now, @now);

-- name: UpdateInboxProject :execrows
UPDATE inbox_projects SET name = @name, color = @color, updated_at = @now WHERE id = @id;

-- name: DeleteInboxProject :execrows
DELETE FROM inbox_projects WHERE id = @id;

-- name: ListTodosOfProject :many
-- The items of a project, in any state: those a removed project leaves without one.
SELECT * FROM todos WHERE project_id = @project_id ORDER BY id;

-- name: ClearTodoProject :execrows
-- An item leaves a removed project (issue #1157), at the revision read, which it moves on.
UPDATE todos SET revision = revision + 1, project_id = NULL, updated_at = @now WHERE id = @id AND revision = @revision;
