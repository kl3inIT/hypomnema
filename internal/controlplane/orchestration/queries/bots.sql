-- name: UpsertBot :exec
INSERT INTO bots (name, provider, model, effort, persona, skills, skills_off, connections, workers, tool_display,
                  created_at, updated_at)
VALUES (@name, @provider, @model, @effort, @persona, @skills, @skills_off, @connections, @workers, @tool_display, @now,
        @now)
ON CONFLICT (name) DO UPDATE SET provider = excluded.provider, model = excluded.model, effort = excluded.effort,
    persona = excluded.persona, skills = excluded.skills, skills_off = excluded.skills_off,
    connections = excluded.connections, workers = excluded.workers, tool_display = excluded.tool_display,
    updated_at = excluded.updated_at;

-- name: GetBot :one
SELECT * FROM bots WHERE name = @name;

-- name: ToolDisplayOfChat :one
-- What the chat's Bot shows of its tool calls (issue #441).
SELECT b.tool_display FROM chats c JOIN bots b ON b.name = c.bot WHERE c.id = @chat_id;

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
