-- name: GetMemory :one
SELECT * FROM memories WHERE kind = @kind AND name = @name;

-- name: SetMemory :exec
INSERT INTO memories (kind, name, memory, updated_at) VALUES (@kind, @name, @memory, @now)
ON CONFLICT (kind, name) DO UPDATE SET memory = excluded.memory, updated_at = excluded.updated_at;
