-- name: InsertClientToken :exec
INSERT INTO client_tokens (id, name, kind, token_hash, created_at) VALUES (@id, @name, @kind, @token_hash, @now);

-- name: GetClientToken :one
SELECT * FROM client_tokens WHERE id = @id;

-- name: GetClientTokenByHash :one
SELECT * FROM client_tokens WHERE token_hash = @token_hash;

-- name: ListClientTokens :many
SELECT * FROM client_tokens ORDER BY name;

-- name: RevokeClientToken :execrows
UPDATE client_tokens SET revoked_at = @now WHERE id = @id AND revoked_at IS NULL;
