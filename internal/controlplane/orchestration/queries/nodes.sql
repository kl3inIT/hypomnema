-- name: InsertNode :exec
INSERT INTO nodes (id, name, status, max_dispatches, created_at, updated_at)
VALUES (@id, @name, 'active', @max_dispatches, @now, @now);

-- name: GetNode :one
SELECT * FROM nodes WHERE id = @id;

-- name: ListNodes :many
-- Every node that has not been removed.
SELECT * FROM nodes WHERE status <> 'deleted' ORDER BY name;

-- name: NodeHello :execrows
-- A machine waited for, or given up on, that says hello is ready; one that was started is on.
UPDATE nodes
SET updated_at = @now, last_seen_at = @now, worktree_root = @worktree_root, runtimes = @runtimes,
    daemon_version = @daemon_version, os = @os, arch = @arch, cpus = @cpus, memory_bytes = @memory_bytes,
    disk_bytes = @disk_bytes, unavailable_at = NULL, terminal = @terminal,
    device = @device, kvm = @kvm, task_branches = @task_branches,
    temp_isolation = sqlc.narg(temp_isolation), temp_isolation_reason = @temp_isolation_reason,
    disk_policy = max(disk_policy, @disk_policy), disk_free_bytes = NULL, disk_sample_at = NULL,
    machine_error = CASE WHEN machine_state IN ('provisioning', 'provision_failed') THEN NULL ELSE machine_error END,
    machine_state = CASE WHEN machine_state IN ('provisioning', 'provision_failed') THEN 'ready' ELSE machine_state END,
    machine_power = CASE WHEN machine_power = 'starting' THEN NULL ELSE machine_power END
WHERE id = @id AND status <> 'deleted';

-- name: TouchNode :exec
UPDATE nodes SET last_seen_at = CAST(@now AS INTEGER) WHERE id = @id;

-- name: SetNodeDisk :execrows
UPDATE nodes SET disk_policy = 1, disk_free_bytes = @disk_free_bytes, disk_floor_bytes = @disk_floor_bytes,
    disk_trim_bytes = @disk_trim_bytes, disk_sample_at = @disk_sample_at
WHERE id = @id AND status <> 'deleted';

-- name: SetNodeDiskAlert :exec
UPDATE nodes SET disk_alert = @disk_alert, disk_alert_until = @disk_alert_until WHERE id = @id;

-- name: SetNodeGitHosts :execrows
UPDATE nodes SET git_hosts = @git_hosts WHERE id = @id AND status <> 'deleted';

-- name: SetNodeRuntimes :execrows
-- The agent CLIs a node has now, said in a heartbeat once one was installed (issue #107).
UPDATE nodes SET runtimes = @runtimes WHERE id = @id AND status <> 'deleted';

-- name: SetNodeTools :execrows
UPDATE nodes SET tools = @tools WHERE id = @id AND status <> 'deleted';

-- name: SetNodeStats :execrows
UPDATE nodes
SET cpu_percent = @cpu_percent, memory_used_bytes = @memory_used_bytes, disk_used_bytes = @disk_used_bytes,
    load1 = @load1, gpus = @gpus, stats_at = @now
WHERE id = @id AND status <> 'deleted';

-- name: SetNodeMaxDispatches :execrows
-- A node's optional cap, NULL for none, which the owner changes after the node was added (issues #660, #731).
UPDATE nodes SET updated_at = @now, max_dispatches = @max_dispatches WHERE id = @id AND status <> 'deleted';

-- name: SetNodeReserved :execrows
-- What the owner keeps back from a node's capacity (issue #731).
UPDATE nodes SET updated_at = @now, reserved_cpu_millis = @reserved_cpu_millis,
    reserved_memory_bytes = @reserved_memory_bytes
WHERE id = @id AND status <> 'deleted';

-- name: SetNodeStatus :execrows
UPDATE nodes SET updated_at = @now, status = @status WHERE id = @id AND status <> 'deleted';

-- name: ResumeNode :execrows
UPDATE nodes SET updated_at = @now, status = 'active', unavailable_at = NULL WHERE id = @id AND status <> 'deleted';

-- name: DeleteNode :execrows
UPDATE nodes SET updated_at = @now, status = 'deleted', deleted_at = @now WHERE id = @id AND status <> 'deleted';

-- name: MarkNodeUnavailable :exec
UPDATE nodes SET updated_at = @now, unavailable_at = @now WHERE id = @id AND unavailable_at IS NULL;

-- name: InsertNodeToken :exec
INSERT INTO node_tokens (id, node_id, name, token_hash, created_at) VALUES (@id, @node_id, @name, @token_hash, @now);

-- name: GetLiveNodeTokenByHash :one
-- The token, when it is not revoked and its node has not been removed nor is being removed (issue #996).
SELECT t.* FROM node_tokens t JOIN nodes n ON n.id = t.node_id
WHERE t.token_hash = @token_hash AND t.revoked_at IS NULL AND n.status <> 'deleted'
  AND coalesce(n.machine_state, '') NOT IN ('deleting', 'delete_failed');

-- name: GetNodeToken :one
SELECT * FROM node_tokens WHERE id = @id;

-- name: ListNodeTokens :many
SELECT * FROM node_tokens WHERE node_id = @node_id ORDER BY created_at, id;

-- name: RevokeNodeToken :execrows
UPDATE node_tokens SET revoked_at = @now, revoke_reason = @reason WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeNodeTokens :exec
UPDATE node_tokens SET revoked_at = @now, revoke_reason = @reason WHERE node_id = @node_id AND revoked_at IS NULL;

-- name: TouchNodeToken :exec
UPDATE node_tokens SET last_used_at = CAST(@now AS INTEGER)
WHERE id = @id AND (last_used_at IS NULL OR last_used_at <= CAST(@before AS INTEGER));

-- name: CountActiveDispatchesOfNode :one
SELECT count(*) FROM dispatches WHERE node_id = @node_id AND status IN (sqlc.slice(statuses));

-- name: CountActiveDispatchesPerNode :many
-- The unsettled Dispatches of each node, with the sum of their requests (issue #731).
SELECT node_id, count(*) AS n, CAST(sum(cpu_millis) AS INTEGER) AS cpu_millis,
       CAST(sum(memory_bytes) AS INTEGER) AS memory_bytes
FROM dispatches WHERE status IN (sqlc.slice(statuses)) GROUP BY node_id;

-- name: CountActiveDispatchesPerBot :many
-- The unsettled Dispatches of each Bot's Runs (issue #660), the Bot found as runBotGives finds it: the Run's own,
-- else, for a Run a coordinator made in a chat, that chat's; '' for the owner's Runs without a Bot.
SELECT CAST(coalesce(r.bot_id, CASE WHEN r.coordinator_token_id IS NOT NULL THEN c.bot_id END, '') AS TEXT) AS bot,
       count(*) AS n
FROM dispatches d
JOIN tasks t ON t.id = d.task_id
JOIN runs r ON r.id = t.run_id
LEFT JOIN chats c ON c.id = r.chat_id
WHERE d.status IN (sqlc.slice(statuses))
GROUP BY 1;

-- name: InsertAccount :exec
INSERT INTO accounts (id, runtime, label, status, position, created_at, updated_at)
VALUES (@id, @runtime, @label, 'active',
        (SELECT coalesce(max(position), 0) + 1 FROM accounts WHERE runtime = @runtime), @now, @now);

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = @id;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY runtime, position, id;

-- name: GetAccountByExternalID :one
SELECT * FROM accounts
WHERE runtime = @runtime AND external_id = @external_id AND lower(coalesce(email, '')) = lower(@email);

-- name: InsertLoginAccount :exec
INSERT INTO accounts (id, runtime, label, status, position, external_id, email, plan, project, secret_ref,
                      created_at, updated_at)
VALUES (@id, @runtime, @label, 'active',
        (SELECT coalesce(max(position), 0) + 1 FROM accounts WHERE runtime = @runtime),
        @external_id, @email, @plan, @project, @secret_ref, @now, @now);

-- name: SetAccountIdentity :execrows
UPDATE accounts SET updated_at = @now, email = @email, plan = @plan WHERE id = @id;

-- name: SetAccountLogin :execrows
UPDATE accounts SET updated_at = @now, email = @email, plan = @plan, project = @project WHERE id = @id;

-- name: SetVerificationLogin :execrows
UPDATE accounts
SET updated_at = @now, verify_login_version = @version,
    verify_url = NULL, verify_message = NULL, verify_asked_at = NULL, verify_failures = 0, verify_retry_at = NULL
WHERE id = @id AND verify_login_version < @version;

-- name: ClaimAccountVerificationRetry :execrows
UPDATE accounts SET updated_at = @now, verify_failures = verify_failures + 1, verify_retry_at = @next_retry_at
WHERE id = @id AND verify_login_version <= @version AND verify_asked_at IS NOT NULL
    AND verify_retry_at = @previous_retry_at AND verify_retry_at <= @now;

-- name: SetAccountVerification :execrows
-- Google asked for the account to be verified (issue #822).
UPDATE accounts
SET updated_at = @now, verify_url = @verify_url, verify_message = @verify_message, verify_asked_at = @verify_asked_at,
    verify_failures = @verify_failures, verify_retry_at = @verify_retry_at
WHERE id = @id AND verify_login_version <= @version;

-- name: ClearAccountVerification :execrows
-- A call under the account succeeded: Google no longer asks.
UPDATE accounts
SET updated_at = @now, verify_url = NULL, verify_message = NULL, verify_asked_at = NULL, verify_failures = 0,
    verify_retry_at = NULL
WHERE id = @id AND verify_login_version <= @version AND verify_asked_at IS NOT NULL;

-- name: AccountLabelTaken :one
SELECT EXISTS (SELECT 1 FROM accounts WHERE runtime = @runtime AND label = @label);

-- name: SetAccountExternalID :execrows
-- A login takes over an account recorded under a former vendor id.
UPDATE accounts SET external_id = @external_id WHERE id = @id;

-- name: SetAccountRefresh :execrows
UPDATE accounts
SET updated_at = @now, refresh_failures = @refresh_failures, refresh_retry_at = @refresh_retry_at,
    refresh_error = @refresh_error
WHERE id = @id;

-- name: DeleteLoginAccount :execrows
DELETE FROM accounts WHERE id = @id AND secret_ref IS NOT NULL;

-- name: ListAccountLabelsBySecretRef :many
SELECT runtime, label FROM accounts WHERE secret_ref = @secret_ref ORDER BY runtime, label;

-- name: SetAccountStatus :execrows
UPDATE accounts SET updated_at = @now, status = @status WHERE id = @id;

-- name: SetAccountPosition :execrows
UPDATE accounts SET updated_at = @now, position = @position WHERE id = @id;

-- name: InsertTokenAccount :exec
-- An account whose pasted token the control plane keeps for the gateway (issues #150, #381).
INSERT INTO accounts (id, runtime, label, status, position, secret_ref, created_at, updated_at)
VALUES (@id, @runtime, @label, 'active',
        (SELECT coalesce(max(position), 0) + 1 FROM accounts WHERE runtime = @runtime),
        @secret_ref, @now, @now);

-- name: CountAccountsFreeAfter :one
-- Active accounts of a runtime that a worker can run under, with no cooldown lasting past @after.
SELECT count(*) FROM accounts a
WHERE a.runtime = @runtime AND a.status = 'active' AND a.secret_ref IS NULL
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

-- name: RenameNode :execrows
UPDATE nodes SET updated_at = @now, name = @name WHERE id = @id AND status <> 'deleted';
