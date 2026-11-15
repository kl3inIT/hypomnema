-- name: InsertBot :exec
INSERT INTO bots (id, name, provider, model, effort, persona, skills, skills_off, connections, workers, tool_display,
                  created_at, updated_at)
VALUES (@id, @name, @provider, @model, @effort, @persona, @skills, @skills_off, @connections, @workers, @tool_display,
        @now, @now);

-- name: UpdateBot :exec
-- A Bot's settings; its name and memory stay.
UPDATE bots SET provider = @provider, model = @model, effort = @effort, persona = @persona, skills = @skills,
    skills_off = @skills_off, connections = @connections, workers = @workers, tool_display = @tool_display,
    updated_at = @now
WHERE id = @id;

-- name: RenameBot :execrows
UPDATE bots SET name = @name, updated_at = @now WHERE id = @id;

-- name: GetBot :one
SELECT * FROM bots WHERE id = @id;

-- name: GetBotByName :one
SELECT * FROM bots WHERE name = @name;

-- name: ToolDisplayOfChat :one
-- What the chat's Bot shows of its tool calls (issue #441).
SELECT b.tool_display FROM chats c JOIN bots b ON b.id = c.bot_id WHERE c.id = @chat_id;

-- name: ListBots :many
SELECT * FROM bots ORDER BY created_at, name;

-- name: DeleteBot :exec
DELETE FROM bots WHERE id = @id;

-- name: SetBotMemory :exec
UPDATE bots SET memory = @memory, updated_at = @now WHERE id = @id;

-- name: CountRepliesOfBotSince :one
-- What a Bot answered since a moment, in chats other than one: that it was used since then.
SELECT count(*) FROM chat_messages m JOIN chats c ON c.id = m.chat_id
WHERE c.bot_id = @bot AND m.role = 'coordinator' AND m.created_at >= @since AND m.chat_id <> @chat_id;

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
