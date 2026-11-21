-- A Bot's requests for a Connection it was not given (issue #471, part D; migration connection_requests).

-- name: InsertConnectionRequest :exec
INSERT INTO connection_requests (id, bot_id, chat_id, integration, label, reason, turn, created_at)
VALUES (@id, @bot_id, @chat_id, @integration, @label, @reason, @turn, @now);

-- name: GetConnectionRequest :one
SELECT * FROM connection_requests WHERE id = @id;

-- name: LatestConnectionRequest :one
-- The newest request of a Bot in a chat for one Connection, whatever its state.
SELECT * FROM connection_requests
WHERE bot_id = @bot_id AND chat_id = @chat_id AND integration = @integration AND label = @label
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListConnectionRequests :many
-- The requests, the newest first, of one state and one chat when they are named.
SELECT * FROM connection_requests
WHERE (CAST(@state AS TEXT) = '' OR state = CAST(@state AS TEXT))
  AND (CAST(@chat_id AS TEXT) = '' OR chat_id = CAST(@chat_id AS TEXT))
ORDER BY created_at DESC, id DESC
LIMIT @max_rows;

-- name: DecideConnectionRequest :execrows
-- The first answer wins: only a pending request is decided.
UPDATE connection_requests SET state = @state, decided_at = @now WHERE id = @id AND state = 'pending';

-- name: DeleteConnectionRequestsOfChat :exec
-- A chat deleted takes its requests (orchestration deleteChatTx).
DELETE FROM connection_requests WHERE chat_id = @chat_id;

-- name: SetBotConnections :exec
-- Always for this Bot appends one use to the Bot's limited list (issue #471, part D): only that column, so no other
-- setting of the Bot changes.
UPDATE bots SET connections = @connections, updated_at = @now WHERE id = @id;

