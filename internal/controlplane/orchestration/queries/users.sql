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
WHERE s.secret_hash = @secret_hash AND s.revoked_at IS NULL;

-- name: ExtendWebSession :exec
-- A session in use: its end moves and its last use is written, at most once an hour.
UPDATE web_sessions SET expires_at = @expires_at, last_used_at = @now WHERE id = @id;

-- name: RevokeWebSession :exec
-- A revoked session is kept until its end passes, as an expired one is (issue #996).
UPDATE web_sessions SET revoked_at = @now, revoke_reason = @reason WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeWebSessionsOfUser :exec
UPDATE web_sessions SET revoked_at = @now, revoke_reason = @reason WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: DeleteExpiredWebSessions :exec
DELETE FROM web_sessions WHERE expires_at <= @now;
