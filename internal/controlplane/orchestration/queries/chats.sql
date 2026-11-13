-- name: InsertChat :exec
INSERT INTO chats (id, title, bot, created_at, updated_at) VALUES (@id, @title, @bot, @now, @now);

-- name: GetChat :one
SELECT * FROM chats WHERE id = @id;

-- name: ListChats :many
SELECT * FROM chats ORDER BY updated_at DESC, id LIMIT @lim;

-- name: TouchChat :exec
UPDATE chats SET updated_at = @now WHERE id = @id;

-- name: InsertChatMessage :one
INSERT INTO chat_messages (chat_id, role, kind, text, parts, attachments, event_seq, created_at)
VALUES (@chat_id, @role, @kind, @text, @parts, @attachments, @event_seq, @now)
RETURNING seq;

-- name: ListChatMessages :many
-- Each wake-up with the event it reports (issue #303): the event never changes, so it is read here rather than kept
-- on the row. Other rows have empty event columns.
SELECT sqlc.embed(chat_messages),
       CAST(coalesce(events.kind, '') AS TEXT)        AS event_kind,
       CAST(coalesce(events.run_id, '') AS TEXT)      AS event_run_id,
       CAST(coalesce(events.task_id, '') AS TEXT)     AS event_task_id,
       CAST(coalesce(events.question_id, '') AS TEXT) AS event_question_id,
       CAST(coalesce(events.payload, '') AS TEXT)     AS event_payload
FROM chat_messages
LEFT JOIN events ON chat_messages.kind = 'wake' AND events.seq = chat_messages.event_seq
WHERE chat_messages.chat_id = @chat_id AND chat_messages.seq > @after
ORDER BY chat_messages.seq
LIMIT @lim;

-- name: UndeliveredChatMessages :many
-- Owner messages and wake-ups not yet given to pi, oldest first.
SELECT * FROM chat_messages
WHERE chat_id = @chat_id AND delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1)
ORDER BY seq;

-- name: ChatsWithUndelivered :many
SELECT DISTINCT chat_id FROM chat_messages
WHERE delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1);

-- name: MarkChatMessagesDelivered :exec
UPDATE chat_messages SET delivered_at = @now
WHERE chat_id = @chat_id AND seq <= @upto AND delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL OR to_coordinator = 1);

-- name: ChatWokenAfter :one
-- The last event this chat was woken for.
SELECT CAST(coalesce(max(event_seq), 0) AS INTEGER) AS woken FROM chat_messages WHERE chat_id = @chat_id;

-- name: ChatsWithRuns :many
SELECT DISTINCT chat_id FROM runs WHERE chat_id IS NOT NULL;

-- name: ChatRunEvents :many
-- Events of the chat's Runs after a cursor that need a coordinator (wakeText
-- in chats.go says the same), so events that wake nobody never fill the page.
SELECT e.* FROM events e JOIN runs r ON r.id = e.run_id
WHERE r.chat_id = @chat_id AND e.seq > @after
  AND (
    e.kind IN ('escalation', 'run.finished', 'run.canceled')
    OR (e.kind = 'task.status'
        AND json_extract(e.payload, '$.to') IN ('succeeded', 'failed', 'blocked', 'canceled'))
    OR (e.kind = 'question.opened' AND json_extract(e.payload, '$.addressed_to') = 'coordinator')
  )
ORDER BY e.seq
LIMIT @lim;

-- name: TitleUntitledChat :exec
-- A chat still under the default title takes one from its first owner message.
UPDATE chats SET title = @title WHERE id = @id AND title = 'Chat';

-- name: InsertChatNotice :exec
-- A message of the control plane's own that the coordinator is given as a wake-up, though no event of a Run is
-- behind it: the owner answered an Approval (issue #154).
INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
VALUES (@chat_id, 'system', 'approval', @text, 1, @now);

-- name: CountChatsOfBot :one
SELECT count(*) FROM chats WHERE bot = @bot;
