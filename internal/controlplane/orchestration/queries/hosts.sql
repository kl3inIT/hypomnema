-- name: UpsertGitHost :exec
INSERT INTO git_hosts (host, kind, created_at) VALUES (@host, @kind, @now)
ON CONFLICT (host) DO UPDATE SET kind = excluded.kind;

-- name: GetGitHost :one
SELECT * FROM git_hosts WHERE host = @host;

-- name: ListGitHosts :many
SELECT * FROM git_hosts ORDER BY host;

-- name: DeleteGitHost :execrows
DELETE FROM git_hosts WHERE host = @host;

-- name: CountOpenRunsOnHost :one
SELECT count(*) FROM runs WHERE status = 'open' AND repo IS NOT NULL AND coalesce(repo_host, 'github.com') = @host;

-- name: CountReposOnHost :one
SELECT count(*) FROM repos WHERE host = @host;

-- name: UpsertRepo :exec
INSERT INTO repos (name, host, path, base, connection, created_at)
VALUES (@name, @host, @path, @base, @connection, @now)
ON CONFLICT (name) DO UPDATE
SET host = excluded.host, path = excluded.path, base = excluded.base, connection = excluded.connection;

-- name: GetRepo :one
SELECT * FROM repos WHERE name = @name;

-- name: ListRepos :many
SELECT * FROM repos ORDER BY name;

-- name: DeleteRepo :execrows
DELETE FROM repos WHERE name = @name;

-- name: UpsertConnection :exec
INSERT INTO connections (integration, label, account, secret, services, address, version, created_at, updated_at)
VALUES (@integration, @label, @account, @secret, @services, @address, @version, @now, @now)
ON CONFLICT (integration, label) DO UPDATE
SET account = excluded.account, secret = excluded.secret, services = excluded.services,
    address = excluded.address, version = excluded.version, sign_in_failed = 0,
    updated_at = excluded.updated_at;

-- name: SetConnectionSignInFailed :execrows
-- Whether the service refused the account the last time the control plane signed in. Only a change is written.
UPDATE connections SET sign_in_failed = @failed, updated_at = @now
WHERE integration = @integration AND label = @label AND sign_in_failed <> @failed;

-- name: SetConnectionChats :execrows
UPDATE connections SET chats = @chats, updated_at = @now WHERE integration = @integration AND label = @label;

-- name: InsertAgentSession :exec
INSERT INTO agent_sessions (id, key_hash, dispatch_id, uses, created_at)
VALUES (@id, @key_hash, @dispatch_id, @uses, @now);

-- name: GetAgentSessionByHash :one
SELECT * FROM agent_sessions WHERE key_hash = @key_hash AND revoked_at IS NULL;

-- name: RevokeAgentSessionsOfDispatch :execrows
UPDATE agent_sessions SET revoked_at = @now WHERE dispatch_id = @dispatch_id AND revoked_at IS NULL;

-- name: ListDispatchesWithAgentSessions :many
SELECT DISTINCT dispatch_id FROM agent_sessions WHERE revoked_at IS NULL ORDER BY dispatch_id;

-- name: UpsertIntegrationClient :exec
INSERT INTO integration_clients (integration, client_id, secret, created_at, updated_at)
VALUES (@integration, @client_id, @secret, @now, @now)
ON CONFLICT (integration) DO UPDATE
SET client_id = excluded.client_id, secret = excluded.secret, updated_at = excluded.updated_at;

-- name: GetIntegrationClient :one
SELECT * FROM integration_clients WHERE integration = @integration;

-- name: ListIntegrationClientsBySecret :many
SELECT * FROM integration_clients WHERE secret = @secret ORDER BY integration;

-- name: GetConnection :one
SELECT * FROM connections WHERE integration = @integration AND label = @label;

-- name: ListConnections :many
SELECT * FROM connections ORDER BY integration, created_at, label;

-- name: ListConnectionsOf :many
SELECT * FROM connections WHERE integration = @integration ORDER BY created_at, label;

-- name: InsertSecretRead :exec
INSERT INTO secret_reads (secret, token_id, created_at) VALUES (@secret, @token_id, @now);

-- name: ListSecretReads :many
SELECT * FROM secret_reads WHERE secret = @secret ORDER BY id DESC LIMIT @lim;

-- name: ListConnectionsBySecret :many
SELECT * FROM connections WHERE secret = @secret ORDER BY integration, label;

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE integration = @integration AND label = @label;

-- name: CountReposUsingConnection :one
SELECT count(*) FROM repos WHERE host = @host AND connection = @connection;

-- name: ConnectionOfRepo :one
-- The Connection a repository registered at this host and path names; several names may register one repository.
SELECT connection FROM repos
WHERE host = @host AND path = @path AND connection IS NOT NULL ORDER BY name LIMIT 1;
