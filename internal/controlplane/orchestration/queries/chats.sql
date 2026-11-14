-- name: InsertChat :exec
INSERT INTO chats (id, title, bot, provider, model, effort, created_at, updated_at)
VALUES (@id, @title, @bot, @provider, @model, @effort, @now, @now);

-- name: SetChatModel :exec
-- The chat's own model and effort (issue #369); NULL is the Bot's.
UPDATE chats SET provider = @provider, model = @model, effort = @effort WHERE id = @id;

-- name: SetChatContextUsage :exec
-- pi's last report of the chat's context window (issue #384); the chat's updated_at stays, as it orders the list by
-- what was said.
UPDATE chats SET context_tokens = @tokens, context_window = @context_window, context_percent = @percent
WHERE id = @id;

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
-- on the row. Other rows have empty event columns. An answered Approval's row is read with its Approval, which no
-- longer changes once answered (issue #465).
SELECT sqlc.embed(chat_messages),
       CAST(coalesce(events.kind, '') AS TEXT)        AS event_kind,
       CAST(coalesce(events.run_id, '') AS TEXT)      AS event_run_id,
       CAST(coalesce(events.task_id, '') AS TEXT)     AS event_task_id,
       CAST(coalesce(events.question_id, '') AS TEXT) AS event_question_id,
       CAST(coalesce(events.payload, '') AS TEXT)     AS event_payload,
       CAST(coalesce(artifacts.id, '') AS TEXT)       AS artifact_id,
       CAST(coalesce(artifacts.revision, 0) AS INTEGER) AS artifact_revision,
       CAST(coalesce(artifacts.updated_at, 0) AS INTEGER) AS artifact_updated_at,
       CAST(coalesce(artifact_revisions.title, '') AS TEXT) AS artifact_title,
       CAST(coalesce(artifact_revisions.type, '') AS TEXT)  AS artifact_type,
       CAST(coalesce(approvals.status, '') AS TEXT)      AS approval_status,
       CAST(coalesce(approvals.scope, '') AS TEXT)       AS approval_scope,
       CAST(coalesce(approvals.standing, 0) AS INTEGER)  AS approval_standing,
       CAST(coalesce(approvals.title, '') AS TEXT)       AS approval_title,
       CAST(coalesce(approvals.action_name, '') AS TEXT) AS approval_action_name,
       CAST(coalesce(approvals.decided_at, 0) AS INTEGER) AS approval_decided_at
FROM chat_messages
LEFT JOIN events ON chat_messages.kind = 'wake' AND events.seq = chat_messages.event_seq
LEFT JOIN artifacts ON chat_messages.kind = 'artifact' AND artifacts.message_seq = chat_messages.seq
LEFT JOIN artifact_revisions ON artifact_revisions.artifact_id = artifacts.id
    AND artifact_revisions.revision = artifacts.revision
LEFT JOIN approvals ON chat_messages.kind = 'approval' AND approvals.id = chat_messages.approval_id
WHERE chat_messages.chat_id = @chat_id AND chat_messages.seq > @after
ORDER BY chat_messages.seq
LIMIT @lim;

-- name: ListChatArtifactMessages :many
-- A chat's artifacts' messages with what each shows now (issue #402): a chat's stream sends one again when its
-- artifact was replaced, as the message is updated in place.
SELECT sqlc.embed(chat_messages),
       artifacts.id         AS artifact_id,
       artifacts.revision   AS artifact_revision,
       artifacts.updated_at AS artifact_updated_at,
       artifact_revisions.title AS artifact_title,
       artifact_revisions.type  AS artifact_type
FROM artifacts
JOIN chat_messages ON chat_messages.seq = artifacts.message_seq
JOIN artifact_revisions ON artifact_revisions.artifact_id = artifacts.id
    AND artifact_revisions.revision = artifacts.revision
WHERE artifacts.chat_id = @chat_id
ORDER BY chat_messages.seq;

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

-- name: InsertChatApproval :exec
-- The owner answered an Approval asked in this chat (issue #465): the row stays where the answer was given. pi is
-- given it only once TellChatOfApproval marks it for the coordinator.
INSERT INTO chat_messages (chat_id, role, kind, text, approval_id, created_at)
VALUES (@chat_id, 'system', 'approval', @text, @approval_id, @now);

-- name: TellChatOfApproval :execrows
-- The coordinator is given an answered Approval's row as a wake-up, though no event of a Run is behind it (issue
-- #154): its request had ended, so it must run the command again. Once only.
UPDATE chat_messages SET to_coordinator = 1 WHERE approval_id = @approval_id AND to_coordinator = 0;

-- name: CountChatsOfBot :one
SELECT count(*) FROM chats WHERE bot = @bot;

-- name: ListChatIDsOfBot :many
SELECT id FROM chats WHERE bot = @bot ORDER BY id;

-- A chat deleted (issue #484), on its own or with its Bot (issue #330): what refers to it is let go of or removed
-- first, in this order. Runs, Routines, their runs (routines.sql) and approvals keep their rows and lose the chat; its gateway keys
-- are revoked and kept, so the gateway's usage records stay whole; its artifacts go with their revisions.

-- name: DetachRunsFromChat :exec
UPDATE runs SET chat_id = NULL WHERE chat_id = CAST(@chat_id AS TEXT);

-- name: DetachApprovalsFromChat :exec
UPDATE approvals SET chat_id = NULL WHERE chat_id = CAST(@chat_id AS TEXT);

-- name: RevokeGatewayKeysOfChat :exec
UPDATE gateway_keys SET revoked_at = coalesce(revoked_at, @now), chat_id = NULL
WHERE chat_id = CAST(@chat_id AS TEXT);

-- name: DeleteSetupChangesOfChat :exec
DELETE FROM setup_changes WHERE chat_id = @chat_id;

-- name: ListArtifactBlobsOfChat :many
-- The bytes the chat's artifacts keep: those no other artifact keeps go after them.
SELECT DISTINCT f.sha256 FROM artifact_files f JOIN artifacts a ON a.id = f.artifact_id WHERE a.chat_id = @chat_id;

-- name: DeleteArtifactsOfChat :exec
-- Their revisions and files go with them (ON DELETE CASCADE).
DELETE FROM artifacts WHERE chat_id = @chat_id;

-- name: DeleteChatMessages :exec
DELETE FROM chat_messages WHERE chat_id = @chat_id;

-- name: DeleteChat :exec
DELETE FROM chats WHERE id = @id;
