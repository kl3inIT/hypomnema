-- name: InsertChat :exec
INSERT INTO chats (id, title, created_at, updated_at) VALUES (@id, @title, @now, @now);

-- name: GetChat :one
SELECT * FROM chats WHERE id = @id;

-- name: ListChats :many
SELECT * FROM chats ORDER BY updated_at DESC, id LIMIT @lim;

-- name: TouchChat :exec
UPDATE chats SET updated_at = @now WHERE id = @id;

-- name: InsertChatMessage :one
INSERT INTO chat_messages (chat_id, role, text, parts, event_seq, created_at)
VALUES (@chat_id, @role, @text, @parts, @event_seq, @now)
RETURNING seq;

-- name: ListChatMessages :many
SELECT * FROM chat_messages WHERE chat_id = @chat_id AND seq > @after ORDER BY seq LIMIT @lim;

-- name: UndeliveredChatMessages :many
-- Owner messages and wake-ups not yet given to pi, oldest first.
SELECT * FROM chat_messages
WHERE chat_id = @chat_id AND delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL)
ORDER BY seq;

-- name: ChatsWithUndelivered :many
SELECT DISTINCT chat_id FROM chat_messages
WHERE delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL);

-- name: MarkChatMessagesDelivered :exec
UPDATE chat_messages SET delivered_at = @now
WHERE chat_id = @chat_id AND seq <= @upto AND delivered_at IS NULL AND (role = 'owner' OR event_seq IS NOT NULL);

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
