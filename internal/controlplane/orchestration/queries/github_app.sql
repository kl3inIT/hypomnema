-- name: DeleteGitHubAppInstallations :exec
DELETE FROM github_app_installations;

-- name: PutGitHubAppInstallation :exec
INSERT INTO github_app_installations (repository, repository_id, installation_id, account, updated_at)
VALUES (@repository, @repository_id, @installation_id, @account, @now)
ON CONFLICT (repository) DO UPDATE
SET repository_id = excluded.repository_id, installation_id = excluded.installation_id, account = excluded.account,
    updated_at = excluded.updated_at;

-- name: GitHubAppInstallationOf :one
SELECT installation_id, repository_id FROM github_app_installations WHERE repository = @repository;

-- name: ListGitHubAppInstallations :many
SELECT * FROM github_app_installations ORDER BY repository;

-- name: RecordGitHubDelivery :execrows
-- A delivery already recorded is left as it is: zero rows says it was handled before.
INSERT INTO github_deliveries (id, event, action, repository, payload, received_at)
VALUES (@id, @event, @action, @repository, @payload, @now)
ON CONFLICT (id) DO NOTHING;

-- name: DeleteGitHubDeliveriesBefore :exec
DELETE FROM github_deliveries WHERE received_at < @before;

-- name: LatestGitHubDelivery :one
SELECT id, event, action, repository, received_at FROM github_deliveries
ORDER BY received_at DESC, id DESC LIMIT 1;

-- name: WatchPullRequestCI :exec
INSERT INTO pull_request_ci (repository, number, updated_at) VALUES (@repository, @number, @now)
ON CONFLICT (repository, number) DO NOTHING;

-- name: GetPullRequestCI :one
SELECT * FROM pull_request_ci WHERE repository = @repository AND number = @number;

-- name: SetPullRequestCI :execrows
UPDATE pull_request_ci
SET head = @head, state = @state, failed = @failed, read_at = @read_at, version = version + 1, updated_at = @now
WHERE repository = @repository AND number = @number;

-- name: SetPullRequestCIIfUnchanged :execrows
-- What a read of GitHub's found, kept only when the row is still at the version the read began from: an event that
-- changed it meanwhile (a new head) wins over the read.
UPDATE pull_request_ci
SET head = @head, state = @state, failed = @failed, read_at = @now, version = version + 1, updated_at = @now
WHERE repository = @repository AND number = @number AND version = @version;

-- name: DeletePullRequestCIBefore :exec
DELETE FROM pull_request_ci WHERE updated_at < @before;
