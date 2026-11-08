-- name: InsertNode :exec
INSERT INTO nodes (id, name, token_hash, status, max_dispatches, created_at, updated_at)
VALUES (@id, @name, @token_hash, 'active', @max_dispatches, @now, @now);

-- name: GetNode :one
SELECT * FROM nodes WHERE id = @id;

-- name: GetNodeByTokenHash :one
SELECT * FROM nodes WHERE token_hash = @token_hash;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY name;

-- name: NodeHello :execrows
UPDATE nodes
SET updated_at = @now, last_seen_at = @now, worktree_root = @worktree_root, runtimes = @runtimes,
    unavailable_at = NULL
WHERE id = @id;

-- name: TouchNode :exec
UPDATE nodes SET last_seen_at = CAST(@now AS INTEGER) WHERE id = @id;

-- name: SetNodeStatus :execrows
UPDATE nodes SET updated_at = @now, status = @status WHERE id = @id;

-- name: ResumeNode :execrows
UPDATE nodes SET updated_at = @now, status = 'active', unavailable_at = NULL WHERE id = @id;

-- name: MarkNodeUnavailable :exec
UPDATE nodes SET updated_at = @now, unavailable_at = @now WHERE id = @id AND unavailable_at IS NULL;

-- name: CountActiveDispatches :one
SELECT count(*) FROM dispatches WHERE status IN (sqlc.slice(statuses));

-- name: CountActiveDispatchesPerNode :many
SELECT node_id, count(*) AS n FROM dispatches WHERE status IN (sqlc.slice(statuses)) GROUP BY node_id;

-- name: InsertAccount :exec
INSERT INTO accounts (id, runtime, label, status, position, created_at, updated_at)
VALUES (@id, @runtime, @label, 'active',
        (SELECT coalesce(max(position), 0) + 1 FROM accounts WHERE runtime = @runtime), @now, @now);

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = @id;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY runtime, position, id;

-- name: SetAccountStatus :execrows
UPDATE accounts SET updated_at = @now, status = @status WHERE id = @id;

-- name: CountAccountsFreeAfter :one
-- Active accounts of a runtime with no cooldown lasting past @after.
SELECT count(*) FROM accounts a
WHERE a.runtime = @runtime AND a.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM account_cooldowns c
                  WHERE c.runtime = a.runtime AND c.account_ref = a.id AND c.until > @after);

-- name: ListActiveCooldowns :many
SELECT * FROM account_cooldowns WHERE until > @now;

-- name: ListSchedulableTaskIDs :many
-- Ready Tasks of open Runs, oldest Run first, then by position.
SELECT t.id FROM tasks t JOIN runs r ON r.id = t.run_id
WHERE t.status = 'ready' AND r.status = 'open' AND t.cancel_requested_at IS NULL
  AND (t.not_before IS NULL OR t.not_before <= CAST(@now AS INTEGER))
ORDER BY r.created_at, r.id, t.position;

-- name: CountTaskEscalationsSince :one
SELECT count(*) FROM events
WHERE task_id = @task_id AND kind = 'escalation' AND json_extract(payload, '$.kind') = CAST(@kind AS TEXT)
  AND created_at >= @since;
