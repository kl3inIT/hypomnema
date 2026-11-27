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
SET head = @head, state = @state, failed = @failed, read_at = @read_at, completed = @completed,
    version = version + 1, updated_at = @now
WHERE repository = @repository AND number = @number;

-- name: SetPullRequestCIIfUnchanged :execrows
-- What a read of GitHub's found, kept when the row is still at the version the read began from. When another write
-- came first, the read is kept only if it found the same head the row now has and that head's checks passed or
-- failed, and the row either says pending (a head's checks go from pending to ended: review round 3) or ended
-- earlier, by GitHub's completed time (a rerun of the head: review round 4), compared only when both have one: a
-- result without a time never loses to one with a time (review round 5). A read that found the pull request
-- merged or closed never wins this way: those come from the pull_request events, and a reopen's pending stands
-- (review round 4). A new head's reset (another head) wins over the read.
UPDATE pull_request_ci
SET head = @head, state = @state, failed = @failed, read_at = @now, completed = @completed, version = version + 1,
    updated_at = @now
WHERE repository = @repository AND number = @number
  AND (version = @version
    OR (head = @head AND (@state = 'passed' OR @state = 'failed')
      AND (state = 'pending'
        OR (state IN ('passed', 'failed') AND completed <> '' AND @completed <> '' AND @completed > completed))));

-- name: DeletePullRequestCIBefore :exec
DELETE FROM pull_request_ci WHERE updated_at < @before;
