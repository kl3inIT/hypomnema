-- name: InsertConversation :exec
INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, status, created_at, updated_at)
VALUES (@id, @task_id, @node_id, @worktree, @runtime, @account_ref, 'open', @now, @now);

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = @id;

-- name: LatestConversationOfTask :one
SELECT * FROM conversations WHERE task_id = @task_id ORDER BY created_at DESC, rowid DESC LIMIT 1;

-- name: ListChatWorktrees :many
-- The worktree of each Task of a chat's Runs that has had one: its newest Conversation's, with its node's name.
SELECT t.id AS task_id, t.run_id, t.profile, t.spec, t.status, c.node_id, c.worktree, n.name AS node_name
FROM runs r
JOIN tasks t ON t.run_id = r.id
JOIN conversations c ON c.id = (
    SELECT c2.id FROM conversations c2 WHERE c2.task_id = t.id ORDER BY c2.created_at DESC, c2.rowid DESC LIMIT 1)
JOIN nodes n ON n.id = c.node_id
WHERE r.chat_id = @chat_id
ORDER BY r.created_at, r.id, t.position;

-- name: CloseConversation :execrows
UPDATE conversations SET status = 'closed', close_reason = @close_reason, updated_at = @now
WHERE id = @id AND status = 'open';

-- name: SetCLISessionRef :exec
UPDATE conversations SET cli_session_ref = @cli_session_ref, updated_at = @now WHERE id = @id;

-- name: InsertDispatch :exec
INSERT INTO dispatches (id, task_id, conversation_id, node_id, status, recovery, gate_question_id, follow_up_id,
                        model, effort, profile_version_id, fallback_for, fallback_until, handoff, cpu_millis,
                        memory_bytes, created_at, updated_at, role_chain, chain_entry)
VALUES (@id, @task_id, @conversation_id, @node_id, 'queued', @recovery, @gate_question_id, @follow_up_id,
        @model, @effort, @profile_version_id, @fallback_for, @fallback_until, @handoff, @cpu_millis, @memory_bytes,
        @now, @now, @role_chain, @chain_entry);

-- name: CountFallbacksOfTask :one
-- The Dispatches of a Task that ran under a backup of its profile: a Task is handed off once.
SELECT count(*) FROM dispatches WHERE task_id = @task_id AND fallback_for IS NOT NULL AND declined_low_disk = 0;

-- name: PreviousDispatchOfTask :one
-- The Dispatch of a Task before this one: the worker a hand-off takes over from.
SELECT p.* FROM dispatches p JOIN dispatches d ON d.task_id = p.task_id
WHERE d.id = @id AND p.declined_low_disk = 0 AND (p.created_at < d.created_at OR (p.created_at = d.created_at AND p.rowid < d.rowid))
ORDER BY p.created_at DESC, p.rowid DESC LIMIT 1;

-- name: CountFallbacksOfRun :one
-- The Dispatches of a Run that ran under a backup of a profile in one stretch of its limit.
SELECT count(*) FROM dispatches d JOIN tasks t ON t.id = d.task_id
WHERE t.run_id = @run_id AND d.fallback_for = @fallback_for AND d.fallback_until = @fallback_until AND d.declined_low_disk = 0;

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

-- name: WorktreeDispatches :many
SELECT d.id, d.status, d.accepted_at, d.settled_at, d.declined_low_disk, t.id AS task_id, r.id AS run_id, r.status AS run_status, r.repo
FROM conversations c JOIN dispatches d ON d.conversation_id = c.id
JOIN tasks t ON c.task_id = t.id JOIN runs r ON t.run_id = r.id
WHERE c.node_id = @node_id AND c.worktree = @worktree;

-- name: ConversationHadAnotherAccept :one
SELECT EXISTS (SELECT 1 FROM dispatches
WHERE conversation_id = @conversation_id AND id != @id AND accepted_at IS NOT NULL AND declined_low_disk = 0);

-- name: ProtectedWorktrees :many
SELECT DISTINCT c.worktree FROM conversations c JOIN tasks t ON t.id = c.task_id JOIN runs r ON r.id = t.run_id
WHERE c.node_id = @node_id AND (r.status = 'open' OR EXISTS (
  SELECT 1 FROM dispatches d WHERE d.conversation_id = c.id AND d.settled_at IS NULL));

-- name: AcceptDispatch :execrows
UPDATE dispatches
SET updated_at = @now, status = 'starting', epoch = epoch + 1, accepted_at = @now,
    lease_expires_at = CAST(@lease_expires_at AS INTEGER)
WHERE dispatches.id = @id AND dispatches.status = 'queued' AND dispatches.node_id = @node_id
  AND dispatches.created_at > @queued_after
  AND EXISTS (SELECT 1 FROM tasks t JOIN nodes n ON n.id = dispatches.node_id WHERE t.id = dispatches.task_id
    AND (n.task_branches <> 0 OR (t.push_branch IS NULL AND t.start_from IS NULL AND t.pr_base IS NULL)));

-- name: RenewLease :execrows
-- A lease that has run out is never renewed. The first heartbeat after a stop request acknowledges it.
UPDATE dispatches
SET updated_at       = @now,
    lease_expires_at = CAST(@lease_expires_at AS INTEGER),
    stop_acked_at    = CASE WHEN status = 'stopping' THEN coalesce(stop_acked_at, @now) ELSE stop_acked_at END
WHERE id = @id AND epoch = @epoch AND status IN ('starting', 'running', 'stopping') AND lease_expires_at > @now;

-- name: RetainLease :execrows
-- A node's hello renews the lease of a Dispatch it is told to retain, even one that ran out while the control plane
-- was down and before the reaper marked it unknown (issue #833).
UPDATE dispatches
SET updated_at = @now, lease_expires_at = CAST(@lease_expires_at AS INTEGER)
WHERE id = @id AND epoch = @epoch AND status IN ('starting', 'running', 'stopping');

-- name: MarkStarted :execrows
UPDATE dispatches
SET updated_at = @now, status = 'running', started_at = @now, progress_at = @now, process_ref = @process_ref
WHERE id = @id AND epoch = @epoch AND status = 'starting';

-- name: MarkProgress :execrows
UPDATE dispatches SET updated_at = @now, progress_at = @now, stalled_at = NULL
WHERE id = @id AND epoch = @epoch AND status = 'running';

-- name: SetStalledReason :execrows
-- The worker loops (issue #359): the reason is kept so that the stop it asks for ends the Dispatch failed.
UPDATE dispatches SET updated_at = @now, stalled_reason = @reason
WHERE id = @id AND epoch = @epoch AND status = 'running';

-- name: RecentAttemptEvidence :many
-- The Task's last settled Dispatches, newest first, with how many tool results each had and how many succeeded
-- (issue #359). A tool result's item has "failed": true only when it failed.
SELECT d.id, d.status,
       CAST((SELECT count(*) FROM dispatch_items i WHERE i.dispatch_id = d.id AND i.kind = 'tool_result') AS INTEGER)
           AS results,
       CAST((SELECT count(*) FROM dispatch_items i
             WHERE i.dispatch_id = d.id AND i.kind = 'tool_result'
               AND coalesce(json_extract(i.data, '$.failed'), 0) = 0) AS INTEGER) AS ok
FROM dispatches d
WHERE d.task_id = @task_id AND d.settled_at IS NOT NULL
ORDER BY d.settled_at DESC, d.rowid DESC
LIMIT @lim;

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

-- name: NodesWithRecentTaskExpiries :many
SELECT DISTINCT node_id FROM dispatches
WHERE task_id = @task_id AND status = 'expired' AND declined_low_disk = 0 AND settled_at >= CAST(@since AS INTEGER);

-- name: CountRecentNodeExpiries :one
SELECT count(*) FROM dispatches
WHERE node_id = @node_id AND status = 'expired' AND declined_low_disk = 0 AND settled_at >= CAST(@since AS INTEGER);

-- name: MarkDispatchDeclinedLowDisk :exec
UPDATE dispatches SET declined_low_disk = 1 WHERE id = @id;

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

-- name: RecordPublishedHead :exec
INSERT INTO dispatch_effects (dispatch_id, seq, kind, ref, phase, payload, created_at, updated_at)
SELECT @dispatch_id, (SELECT coalesce(max(seq), 0) + 1 FROM dispatch_effects WHERE dispatch_id = @dispatch_id),
       'git_push', @branch, 'done', @sha, @now, @now
WHERE NOT EXISTS (SELECT 1 FROM dispatch_effects WHERE dispatch_id = @dispatch_id
                  AND kind = 'git_push' AND ref = @branch AND phase = 'done' AND payload = @sha);

-- name: ListPublishedHeads :many
SELECT DISTINCT e.ref AS branch, CAST(e.payload AS TEXT) AS sha
FROM tasks t JOIN dispatches d ON d.task_id = t.id JOIN dispatch_effects e ON e.dispatch_id = d.id
WHERE t.run_id = @run_id AND e.kind = 'git_push' AND e.phase = 'done' AND e.payload IS NOT NULL
ORDER BY branch, sha;

-- name: UpsertCooldown :exec
INSERT INTO account_cooldowns (runtime, account_ref, until, reason)
VALUES (@runtime, @account_ref, @until, @reason)
ON CONFLICT (runtime, account_ref) DO UPDATE
SET until = max(account_cooldowns.until, excluded.until), reason = excluded.reason;

-- name: DeleteExpiredCooldowns :execrows
DELETE FROM account_cooldowns WHERE until <= @now;
