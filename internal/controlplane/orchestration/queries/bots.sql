-- name: InsertBot :exec
-- A new Bot goes last in the order (issue #470).
INSERT INTO bots (id, name, provider, model, effort, instructions, skills, skills_off, tools, tools_off, connections,
                  workers, tool_display, position, connection_scopes, guaranteed_dispatches, preset, created_at, updated_at, fallbacks,
                  owner_accounts)
VALUES (@id, @name, @provider, @model, @effort, @instructions, @skills, @skills_off, @tools, @tools_off, @connections,
        @workers, @tool_display, (SELECT coalesce(max(position) + 1, 0) FROM bots), @connection_scopes,
        @guaranteed_dispatches, @preset, @now, @now, @fallbacks, @owner_accounts);

-- name: UpdateBot :exec
-- A Bot's settings; its name and memory stay.
UPDATE bots SET provider = @provider, model = @model, effort = @effort, instructions = @instructions, skills = @skills,
    skills_off = @skills_off, tools = @tools, tools_off = @tools_off, connections = @connections, workers = @workers, tool_display = @tool_display,
    connection_scopes = @connection_scopes, guaranteed_dispatches = @guaranteed_dispatches, preset = @preset, updated_at = @now,
    fallbacks = @fallbacks
WHERE id = @id;

-- name: SetBotOwnerAccounts :exec
-- What a Bot may do with the owner's own accounts (issue #1023); only the owner sets it, so UpdateBot, which a
-- Bot's change and its undo use, never does.
UPDATE bots SET owner_accounts = @owner_accounts, updated_at = @now WHERE id = @id;

-- name: RenameBot :execrows
UPDATE bots SET name = @name, updated_at = @now WHERE id = @id;

-- name: GetBot :one
SELECT * FROM bots WHERE id = @id;

-- name: ToolDisplayOfChat :one
-- What the chat's Bot shows of its tool calls (issue #441).
SELECT b.tool_display FROM chats c JOIN bots b ON b.id = c.bot_id WHERE c.id = @chat_id;

-- name: ListBots :many
SELECT * FROM bots ORDER BY created_at, name;

-- name: DeleteBot :exec
DELETE FROM bots WHERE id = @id;

-- name: KeepRemovedBotName :exec
-- A removed Bot's name stays with its id (issue #1139).
INSERT INTO removed_bots (id, name) VALUES (@id, @name)
ON CONFLICT (id) DO UPDATE SET name = excluded.name;

-- name: ForgetRemovedBotName :exec
-- A Bot put back by an undo is no longer removed.
DELETE FROM removed_bots WHERE id = @id;

-- name: ListRemovedBots :many
SELECT * FROM removed_bots ORDER BY id;

-- name: CloseBotPosition :exec
-- The Bots after a removed one move up a place, so the order stays 0 to n-1 (issue #470).
UPDATE bots SET position = position - 1 WHERE position > @position;

-- name: ListBotIDsInOrder :many
-- The Bots in the order the owner set (issue #470).
SELECT id FROM bots ORDER BY position, created_at, name;

-- name: SetBotPosition :exec
UPDATE bots SET position = @position WHERE id = @id;

-- name: SetBotDepartment :execrows
-- The department a Bot is listed in, or none (issue #471).
UPDATE bots SET department = @department WHERE id = @id;

-- name: InsertDepartment :exec
-- A new department goes last (issue #471).
INSERT INTO departments (id, name, position, created_at)
VALUES (@id, @name, (SELECT coalesce(max(position) + 1, 0) FROM departments), @now);

-- name: ListDepartments :many
SELECT * FROM departments ORDER BY position, created_at, name;

-- name: GetDepartment :one
SELECT * FROM departments WHERE id = @id;

-- name: RenameDepartment :execrows
UPDATE departments SET name = @name WHERE id = @id;

-- name: DeleteDepartment :exec
-- Its Bots go back to the list of Bots in no department.
DELETE FROM departments WHERE id = @id;

-- name: CloseDepartmentPosition :exec
-- The departments after a removed one move up a place, so the order stays 0 to n-1.
UPDATE departments SET position = position - 1 WHERE position > @position;

-- name: SetDepartmentPosition :exec
UPDATE departments SET position = @position WHERE id = @id;

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
-- In the order they were made: rowid orders the changes made in one millisecond, whose ids are random (issue #758).
SELECT * FROM setup_changes WHERE chat_id = @chat_id ORDER BY created_at, rowid;

-- name: LastSetupChangeOf :one
-- The newest change of a Bot or a profile: an undo applies to that one only.
SELECT * FROM setup_changes WHERE kind = @kind AND name = @name ORDER BY created_at DESC, rowid DESC LIMIT 1;

-- name: MarkSetupChangeUndone :exec
UPDATE setup_changes SET undone_at = @now WHERE id = @id AND undone_at IS NULL;

-- name: ListBotModels :many
-- The model each Bot runs, for a check of the provider it names; no other column, so the check reads any schema
-- since bots had a model.
SELECT name, provider, model FROM bots ORDER BY created_at, name;

-- name: InsertBotVersion :exec
-- One save of a Bot's settings (issue #1336); its number follows the Bot's last.
INSERT INTO bot_versions (id, bot_id, version, settings, source, saved_by, chat_id, restored_from, created_at)
VALUES (@id, @bot_id, (SELECT coalesce(max(version), 0) + 1 FROM bot_versions WHERE bot_id = @bot_id), @settings,
        @source, @saved_by, @chat_id, @restored_from, @now);

-- name: CountBotVersions :one
SELECT count(*) FROM bot_versions WHERE bot_id = @bot_id;

-- name: ListBotVersions :many
SELECT * FROM bot_versions WHERE bot_id = @bot_id ORDER BY version DESC;

-- name: GetBotVersion :one
SELECT * FROM bot_versions WHERE id = @id AND bot_id = @bot_id;
