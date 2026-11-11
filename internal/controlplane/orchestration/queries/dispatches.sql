-- name: InsertConversation :exec
INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, status, created_at, updated_at)
VALUES (@id, @task_id, @node_id, @worktree, @runtime, @account_ref, 'open', @now, @now);

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = @id;

-- name: LatestConversationOfTask :one
SELECT * FROM conversations WHERE task_id = @task_id ORDER BY created_at DESC, rowid DESC LIMIT 1;

-- name: CloseConversation :execrows
UPDATE conversations SET status = 'closed', close_reason = @close_reason, updated_at = @now
WHERE id = @id AND status = 'open';

-- name: SetCLISessionRef :exec
UPDATE conversations SET cli_session_ref = @cli_session_ref, updated_at = @now WHERE id = @id;

-- name: InsertDispatch :exec
INSERT INTO dispatches (id, task_id, conversation_id, node_id, status, recovery, gate_question_id,
                        model, effort, profile_version_id, fallback_for, fallback_until, created_at, updated_at)
VALUES (@id, @task_id, @conversation_id, @node_id, 'queued', @recovery, @gate_question_id,
        @model, @effort, @profile_version_id, @fallback_for, @fallback_until, @now, @now);

-- name: CountFallbacksOfRun :one
-- The Dispatches of a Run that ran under a backup of a profile in one stretch of its limit.
SELECT count(*) FROM dispatches d JOIN tasks t ON t.id = d.task_id
WHERE t.run_id = @run_id AND d.fallback_for = @fallback_for AND d.fallback_until = @fallback_until;

-- name: GetDispatch :one
SELECT * FROM dispatches WHERE id = @id;

-- name: LastDispatchOfTask :one
SELECT * FROM dispatches WHERE task_id = @task_id ORDER BY created_at DESC, rowid DESC LIMIT 1;

-- name: StartWaiting :exec
UPDATE dispatches SET updated_at = @now, waiting_since = coalesce(waiting_since, @now) WHERE id = @id;

-- name: StopWaiting :exec
-- The wait is over: its time leaves the duration limit and the stall timer starts again.
UPDATE dispatches
SET updated_at = @now, waited_ms = waited_ms + (@now - waiting_since), waiting_since = NULL, progress_at = @now,
    stalled_at = NULL
WHERE id = @id AND waiting_since IS NOT NULL;

-- name: ActiveDispatchOfTask :one
SELECT * FROM dispatches
WHERE task_id = @task_id AND status IN ('queued', 'starting', 'running', 'stopping', 'unknown', 'stop_unknown');

-- name: LastDispatchOfConversation :one
SELECT * FROM dispatches WHERE conversation_id = @conversation_id ORDER BY created_at DESC, rowid DESC LIMIT 1;

-- name: AcceptDispatch :execrows
UPDATE dispatches
SET updated_at = @now, status = 'starting', epoch = epoch + 1, accepted_at = @now,
    lease_expires_at = CAST(@lease_expires_at AS INTEGER)
WHERE id = @id AND status = 'queued' AND node_id = @node_id AND created_at > @queued_after;

-- name: RenewLease :execrows
-- A lease that has run out is never renewed. The first heartbeat after a stop request acknowledges it.
UPDATE dispatches
SET updated_at       = @now,
    lease_expires_at = CAST(@lease_expires_at AS INTEGER),
    stop_acked_at    = CASE WHEN status = 'stopping' THEN coalesce(stop_acked_at, @now) ELSE stop_acked_at END
WHERE id = @id AND epoch = @epoch AND status IN ('starting', 'running', 'stopping') AND lease_expires_at > @now;

-- name: MarkStarted :execrows
UPDATE dispatches
SET updated_at = @now, status = 'running', started_at = @now, progress_at = @now, process_ref = @process_ref
WHERE id = @id AND epoch = @epoch AND status = 'starting';

-- name: MarkProgress :execrows
UPDATE dispatches SET updated_at = @now, progress_at = @now, stalled_at = NULL
WHERE id = @id AND epoch = @epoch AND status = 'running';

-- name: RequestStop :execrows
UPDATE dispatches SET updated_at = @now, status = 'stopping', stop_requested_at = @now
WHERE id = @id AND status IN ('starting', 'running');

-- name: MarkUnknown :execrows
UPDATE dispatches
SET status = @to_status, unknown_cause = @unknown_cause, lease_expires_at = NULL, updated_at = @now
WHERE id = @id AND status IN (sqlc.slice('from_statuses'));

-- name: SettleDispatch :execrows
UPDATE dispatches
SET updated_at = @now, status = @to_status, failure_class = @failure_class, unknown_cause = NULL,
    result = @result, resets_at = @resets_at, lease_expires_at = NULL, settled_at = @now,
    waited_ms = waited_ms + coalesce(@now - waiting_since, 0), waiting_since = NULL
WHERE id = @id AND status IN (sqlc.slice('from_statuses'));

-- name: Reattach :execrows
UPDATE dispatches
SET updated_at = @now, status = 'running', epoch = epoch + 1, unknown_cause = NULL,
    lease_expires_at = CAST(@lease_expires_at AS INTEGER), progress_at = @now
WHERE id = @id AND status = 'unknown' AND unknown_cause = 'lease_lost' AND epoch = @epoch
  AND node_id = @node_id AND process_ref = CAST(@process_ref AS TEXT);

-- name: ListQueuedSince :many
SELECT id FROM dispatches WHERE status = 'queued' AND created_at <= @created_before ORDER BY created_at;

-- name: ListLeaseExpired :many
SELECT id, status FROM dispatches
WHERE status IN ('starting', 'running', 'stopping') AND lease_expires_at <= CAST(@now AS INTEGER)
ORDER BY lease_expires_at;

-- name: ListStopTimedOut :many
SELECT id FROM dispatches WHERE status = 'stopping' AND stop_acked_at <= CAST(@acked_before AS INTEGER) ORDER BY stop_acked_at;

-- name: MarkStalled :many
UPDATE dispatches SET updated_at = @now, stalled_at = @now
WHERE status = 'running' AND stalled_at IS NULL AND waiting_since IS NULL
  AND progress_at <= CAST(@progress_before AS INTEGER)
RETURNING id, node_id;

-- name: MarkOverrun :many
-- Running time leaves out the time spent waiting for answers, including a wait still under way.
UPDATE dispatches SET updated_at = @now, overrun_at = @now
WHERE status = 'running' AND overrun_at IS NULL
  AND started_at + waited_ms + CAST(@max_ms AS INTEGER) <= coalesce(waiting_since, @now)
RETURNING id, node_id;

-- name: RecentDispatchStatusesOfNode :many
SELECT status FROM dispatches WHERE node_id = @node_id ORDER BY created_at DESC, rowid DESC LIMIT @n;

-- name: GetReceipt :one
SELECT * FROM receipts WHERE caller = @caller AND request_id = @request_id;

-- name: InsertReceipt :exec
INSERT INTO receipts (caller, request_id, method, payload_hash, response, created_at)
VALUES (@caller, @request_id, @method, @payload_hash, @response, @now);

-- name: DeleteReceiptsBefore :execrows
DELETE FROM receipts WHERE created_at < @created_before;

-- name: TrimReceipts :execrows
DELETE FROM receipts WHERE rowid IN (
    SELECT rowid FROM receipts ORDER BY created_at DESC, rowid DESC LIMIT -1 OFFSET @keep);

-- name: RecordEffect :execrows
-- An intent is inserted; a done either completes the intent with the same seq or is inserted on its own.
INSERT INTO dispatch_effects (dispatch_id, seq, kind, ref, phase, payload, created_at, updated_at)
VALUES (@dispatch_id, @seq, @kind, @ref, @phase, @payload, @now, @now)
ON CONFLICT (dispatch_id, seq) DO UPDATE
SET phase = 'done', ref = excluded.ref, payload = excluded.payload, updated_at = excluded.updated_at
WHERE dispatch_effects.phase = 'intent' AND excluded.phase = 'done' AND dispatch_effects.kind = excluded.kind;

-- name: ListEffects :many
SELECT * FROM dispatch_effects WHERE dispatch_id = @dispatch_id ORDER BY seq;

-- name: RecordResource :exec
INSERT INTO dispatch_resources (dispatch_id, kind, ref, custody, created_at)
VALUES (@dispatch_id, @kind, @ref, @custody, @now)
ON CONFLICT (dispatch_id, kind, ref) DO NOTHING;

-- name: ReleaseResource :execrows
UPDATE dispatch_resources SET released_at = CAST(@now AS INTEGER)
WHERE dispatch_id = @dispatch_id AND kind = @kind AND ref = @ref AND released_at IS NULL;

-- name: ListResources :many
SELECT * FROM dispatch_resources WHERE dispatch_id = @dispatch_id ORDER BY created_at, kind, ref;

-- name: UpsertCooldown :exec
INSERT INTO account_cooldowns (runtime, account_ref, until, reason)
VALUES (@runtime, @account_ref, @until, @reason)
ON CONFLICT (runtime, account_ref) DO UPDATE
SET until = max(account_cooldowns.until, excluded.until), reason = excluded.reason;

-- name: GetCooldown :one
SELECT * FROM account_cooldowns WHERE runtime = @runtime AND account_ref = @account_ref AND until > @now;

-- name: DeleteExpiredCooldowns :execrows
DELETE FROM account_cooldowns WHERE until <= @now;
