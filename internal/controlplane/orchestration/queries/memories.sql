-- name: GetMemory :one
SELECT * FROM memories WHERE kind = @kind AND name = @name;

-- name: SetMemory :exec
INSERT INTO memories (kind, name, memory, updated_at) VALUES (@kind, @name, @memory, @now)
ON CONFLICT (kind, name) DO UPDATE SET memory = excluded.memory, updated_at = excluded.updated_at;

-- name: ListMemoryNotes :many
-- The notes of a memory directory (issue #589), by path.
SELECT path, length(CAST(text AS BLOB)) AS bytes, updated_at FROM memory_notes
WHERE kind = @kind AND name = @name ORDER BY path;

-- name: GetMemoryNote :one
SELECT * FROM memory_notes WHERE kind = @kind AND name = @name AND path = @path;

-- name: SetMemoryNote :exec
INSERT INTO memory_notes (kind, name, path, text, updated_at) VALUES (@kind, @name, @path, @text, @now)
ON CONFLICT (kind, name, path) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at;

-- name: DeleteMemoryNote :exec
DELETE FROM memory_notes WHERE kind = @kind AND name = @name AND path = @path;

-- name: DeleteMemoryNotesOf :exec
-- Every note of an agent's or a Bot's memory, when the Bot is removed with its memory.
DELETE FROM memory_notes WHERE kind = @kind AND name = @name;

-- name: CountMemoryNotes :one
SELECT count(*) FROM memory_notes WHERE kind = @kind AND name = @name;
