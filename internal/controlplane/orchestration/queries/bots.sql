-- name: UpsertBot :exec
INSERT INTO bots (name, provider, model, effort, persona, skills, skills_off, connections, workers, created_at,
                  updated_at)
VALUES (@name, @provider, @model, @effort, @persona, @skills, @skills_off, @connections, @workers, @now, @now)
ON CONFLICT (name) DO UPDATE SET provider = excluded.provider, model = excluded.model, effort = excluded.effort,
    persona = excluded.persona, skills = excluded.skills, skills_off = excluded.skills_off,
    connections = excluded.connections, workers = excluded.workers, updated_at = excluded.updated_at;

-- name: GetBot :one
SELECT * FROM bots WHERE name = @name;

-- name: ListBots :many
SELECT * FROM bots ORDER BY created_at, name;

-- name: DeleteBot :exec
DELETE FROM bots WHERE name = @name;

-- name: SetBotMemory :exec
UPDATE bots SET memory = @memory, updated_at = @now WHERE name = @name;

-- name: CountRepliesOfBotSince :one
-- What a Bot answered since a moment, in chats other than one: that it was used since then.
SELECT count(*) FROM chat_messages m JOIN chats c ON c.id = m.chat_id
WHERE c.bot = @bot AND m.role = 'coordinator' AND m.created_at >= @since AND m.chat_id <> @chat_id;

-- name: InsertSetupChange :exec
INSERT INTO setup_changes (id, chat_id, kind, name, action, before, after, created_at)
VALUES (@id, @chat_id, @kind, @name, @action, @before, @after, @now);

-- name: GetSetupChange :one
SELECT * FROM setup_changes WHERE id = @id;

-- name: ListSetupChangesOfChat :many
SELECT * FROM setup_changes WHERE chat_id = @chat_id ORDER BY created_at, id;

-- name: LastSetupChangeOf :one
-- The newest change of a Bot or a profile: an undo applies to that one only.
SELECT * FROM setup_changes WHERE kind = @kind AND name = @name ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: MarkSetupChangeUndone :exec
UPDATE setup_changes SET undone_at = @now WHERE id = @id AND undone_at IS NULL;

-- A Bot removed with its chats (issue #330): what refers to its chats is let go of or removed first, in this order.
-- Runs, routines, their runs and approvals keep their rows and lose the chat; a chat's gateway keys are revoked and
-- kept, so the gateway's usage records stay whole.

-- name: DetachRunsFromChatsOfBot :exec
UPDATE runs SET chat_id = NULL WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DetachRoutinesFromChatsOfBot :exec
UPDATE routines SET chat_id = NULL WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DetachRoutineRunsFromChatsOfBot :exec
UPDATE routine_runs SET chat_id = NULL WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DetachApprovalsFromChatsOfBot :exec
UPDATE approvals SET chat_id = NULL WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: RevokeGatewayKeysOfChatsOfBot :exec
UPDATE gateway_keys SET revoked_at = coalesce(revoked_at, @now), chat_id = NULL
WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DeleteSetupChangesOfChatsOfBot :exec
DELETE FROM setup_changes WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DeleteLarkChannelChatsOfBot :exec
DELETE FROM lark_channel_chats WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DeleteChatMessagesOfBot :exec
DELETE FROM chat_messages WHERE chat_id IN (SELECT c.id FROM chats c WHERE c.bot = @bot);

-- name: DeleteChatsOfBot :exec
DELETE FROM chats WHERE bot = @bot;
