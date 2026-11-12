-- name: UpsertBot :exec
INSERT INTO bots (name, provider, model, effort, persona, created_at, updated_at)
VALUES (@name, @provider, @model, @effort, @persona, @now, @now)
ON CONFLICT (name) DO UPDATE SET provider = excluded.provider, model = excluded.model, effort = excluded.effort,
    persona = excluded.persona, updated_at = excluded.updated_at;

-- name: GetBot :one
SELECT * FROM bots WHERE name = @name;

-- name: ListBots :many
SELECT * FROM bots ORDER BY created_at, name;

-- name: DeleteBot :exec
DELETE FROM bots WHERE name = @name;

-- name: SetBotMemory :exec
UPDATE bots SET memory = @memory, updated_at = @now WHERE name = @name;
