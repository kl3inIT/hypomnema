-- name: InsertClientToken :exec
INSERT INTO client_tokens (id, name, kind, token_hash, created_at) VALUES (@id, @name, @kind, @token_hash, @now);

-- name: GetClientToken :one
SELECT * FROM client_tokens WHERE id = @id;

-- name: GetClientTokenByHash :one
SELECT * FROM client_tokens WHERE token_hash = @token_hash;

-- name: GetClientTokenByName :one
SELECT * FROM client_tokens WHERE name = @name;

-- name: ListClientTokens :many
SELECT * FROM client_tokens ORDER BY name;

-- name: RevokeClientToken :execrows
UPDATE client_tokens SET revoked_at = @now, revoke_reason = @reason WHERE id = @id AND revoked_at IS NULL;

-- name: TouchClientToken :exec
-- A token's last use, written at most once an hour (issue #996), as a node token's is.
UPDATE client_tokens SET last_used_at = CAST(@now AS INTEGER)
WHERE id = @id AND (last_used_at IS NULL OR last_used_at <= CAST(@before AS INTEGER));

-- name: RevokeAgentSessionsOfCoordinatorToken :exec
-- The agent sessions of the Dispatches of the Runs a coordinator token created, as the token is revoked (issue #996).
UPDATE agent_sessions SET revoked_at = @now, revoke_reason = @reason
WHERE revoked_at IS NULL AND dispatch_id IN (
    SELECT d.id FROM dispatches d JOIN tasks t ON t.id = d.task_id JOIN runs r ON r.id = t.run_id
    WHERE r.coordinator_token_id = CAST(@token_id AS TEXT));
