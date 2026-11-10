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
INSERT INTO repos (name, host, path, base, created_at) VALUES (@name, @host, @path, @base, @now)
ON CONFLICT (name) DO UPDATE SET host = excluded.host, path = excluded.path, base = excluded.base;

-- name: GetRepo :one
SELECT * FROM repos WHERE name = @name;

-- name: ListRepos :many
SELECT * FROM repos ORDER BY name;

-- name: DeleteRepo :execrows
DELETE FROM repos WHERE name = @name;

-- name: SetGitHostTokenSecret :exec
-- A known host has no row until it gets a token.
INSERT INTO git_hosts (host, kind, token_secret, created_at) VALUES (@host, @kind, @token_secret, @now)
ON CONFLICT (host) DO UPDATE SET token_secret = excluded.token_secret;

-- name: ClearGitHostTokenSecret :exec
UPDATE git_hosts SET token_secret = NULL WHERE host = @host;
