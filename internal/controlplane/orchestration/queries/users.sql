-- name: InsertUser :exec
INSERT INTO users (id, name, password_hash, created_at, updated_at) VALUES (@id, @name, @password_hash, @now, @now);

-- name: GetUserByName :one
SELECT * FROM users WHERE name = @name;

-- name: ListUsers :many
SELECT * FROM users ORDER BY name;

-- name: SetUserPassword :execrows
UPDATE users SET password_hash = @password_hash, updated_at = @now WHERE name = @name;

-- name: InsertWebSession :exec
INSERT INTO web_sessions (id, user_id, secret_hash, created_at, expires_at)
VALUES (@id, @user_id, @secret_hash, @now, @expires_at);

-- name: GetWebSessionByHash :one
SELECT s.id, s.user_id, s.expires_at, u.name AS user_name
FROM web_sessions s JOIN users u ON u.id = s.user_id
WHERE s.secret_hash = @secret_hash;

-- name: ExtendWebSession :exec
UPDATE web_sessions SET expires_at = @expires_at WHERE id = @id;

-- name: DeleteWebSession :exec
DELETE FROM web_sessions WHERE id = @id;

-- name: DeleteWebSessionsOfUser :exec
DELETE FROM web_sessions WHERE user_id = @user_id;

-- name: DeleteExpiredWebSessions :exec
DELETE FROM web_sessions WHERE expires_at <= @now;
