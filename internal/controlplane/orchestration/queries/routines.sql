-- name: InsertRoutine :exec
INSERT INTO routines (id, bot, name, kind, prompt, script, script_text, every_minutes, anchor_at, cron, timezone,
                      timeout_seconds, state, summary, chat_id, announce, next_run_at, created_by, undo,
                      created_at, updated_at)
VALUES (@id, @bot, @name, @kind, @prompt, @script, @script_text, @every_minutes, @anchor_at, @cron, @timezone,
        @timeout_seconds, 'on', @summary, @chat_id, @announce, @next_run_at, @created_by, @undo, @now, @now);

-- name: UpdateRoutine :exec
-- What a Routine runs, when, and where its result goes; its state and its count of failures stay.
UPDATE routines
SET kind = @kind, prompt = @prompt, script = @script, script_text = @script_text, every_minutes = @every_minutes,
    anchor_at = @anchor_at, cron = @cron, timezone = @timezone, timeout_seconds = @timeout_seconds,
    revision = @revision, summary = @summary, chat_id = @chat_id, announce = @announce, next_run_at = @next_run_at,
    undo = @undo, updated_at = @now
WHERE id = @id;

-- name: GetRoutine :one
SELECT * FROM routines WHERE id = @id;

-- name: GetRoutineByName :one
SELECT * FROM routines WHERE bot = @bot AND name = @name;

-- name: ListRoutines :many
-- Empty filters match everything. The oldest first.
SELECT * FROM routines
WHERE (CAST(@bot AS TEXT) = '' OR bot = @bot)
  AND (CAST(@chat_id AS TEXT) = '' OR chat_id = @chat_id)
ORDER BY created_at, id;

-- name: CountRoutinesOfBot :one
SELECT count(*) FROM routines WHERE bot = @bot;

-- name: DeleteRoutine :exec
DELETE FROM routines WHERE id = @id;

-- name: DueRoutines :many
-- The Routines that are on and whose time has come, the longest overdue first.
SELECT * FROM routines WHERE state = 'on' AND next_run_at IS NOT NULL AND next_run_at <= @now
ORDER BY next_run_at, id;

-- name: SwitchRoutine :exec
-- The owner pauses a Routine or switches it on again; switching on forgets its failures.
UPDATE routines SET state = @state, paused_reason = @paused_reason, next_run_at = @next_run_at, failures = @failures,
    alerted_at = @alerted_at, undo = NULL, updated_at = @now
WHERE id = @id;

-- name: SettleRoutine :exec
-- What a run's end means for its Routine. It is not a change of the Routine: updated_at stays.
UPDATE routines SET state = @state, paused_reason = @paused_reason, next_run_at = @next_run_at, failures = @failures,
    alerted_at = @alerted_at
WHERE id = @id;

-- name: SetRoutineNextRun :exec
UPDATE routines SET next_run_at = @next_run_at WHERE id = @id;

-- name: SkipRoutineTime :exec
-- A due time that passed while a run was live.
UPDATE routines SET next_run_at = @next_run_at, skipped = skipped + 1, last_skipped_at = @skipped_at WHERE id = @id;

-- name: DeleteStaleRoutineGrants :exec
-- An "always for this Routine" ends when what the Routine runs, or when, changed.
DELETE FROM routine_grants WHERE routine_id = @routine_id AND revision <> @revision;

-- name: InsertRoutinePrompt :exec
-- A prompt run's chat opens with the prompt as a wake-up of the control plane, which the coordinator is given
-- though no event of a Run is behind it (issue #267's kinds: a wake-up).
INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
VALUES (@chat_id, 'system', 'wake', @text, 1, @now);

-- name: InsertRoutineRun :exec
INSERT INTO routine_runs (id, routine_id, started_by, state, started_at, chat_id)
VALUES (@id, @routine_id, @started_by, 'running', @now, @chat_id);

-- name: GetRoutineRun :one
SELECT * FROM routine_runs WHERE id = @id;

-- name: ListRoutineRuns :many
-- A page of a Routine's runs, the newest first.
SELECT * FROM routine_runs WHERE routine_id = @routine_id ORDER BY started_at DESC, id DESC LIMIT @lim OFFSET @off;

-- name: CountRoutineRuns :one
SELECT COUNT(*) FROM routine_runs WHERE routine_id = @routine_id;

-- name: LiveRunOfRoutine :one
-- The run of a Routine that is still going or waits for the owner; there is at most one.
SELECT * FROM routine_runs WHERE routine_id = @routine_id AND state IN ('running', 'waiting');

-- name: ListLiveRoutineRuns :many
SELECT * FROM routine_runs WHERE state IN ('running', 'waiting') ORDER BY started_at, id;

-- name: LiveRoutineRunOfChat :one
-- The live run a chat was opened for; a chat is one run's.
SELECT * FROM routine_runs WHERE chat_id = @chat_id AND state IN ('running', 'waiting');

-- name: RoutineRunOfChat :one
-- The run a chat was opened for, live or ended.
SELECT * FROM routine_runs WHERE chat_id = @chat_id ORDER BY started_at DESC LIMIT 1;

-- name: LiveRoutineRunOfKey :one
-- The live run whose launcher was given the key with this digest.
SELECT * FROM routine_runs WHERE key_hash = @key_hash AND state IN ('running', 'waiting');

-- name: ClaimRoutineRunKey :execrows
-- A run's key is given once, to the launcher that starts its script.
UPDATE routine_runs SET key_hash = @key_hash WHERE id = @id AND state = 'running' AND key_hash IS NULL;

-- name: FinishRoutineRun :execrows
-- A run ends once: only one that is still going or waiting.
UPDATE routine_runs SET state = @state, ended_at = @now, exit_code = @exit_code, output = @output, error = @error
WHERE id = @id AND state IN ('running', 'waiting');

-- name: WaitRoutineRun :execrows
UPDATE routine_runs SET state = 'waiting' WHERE id = @id AND state = 'running';

-- name: ResumeRoutineRun :execrows
UPDATE routine_runs SET state = 'running' WHERE id = @id AND state = 'waiting';

-- name: DeleteRoutineRunsBefore :exec
-- Runs that ended are kept for a while.
DELETE FROM routine_runs WHERE ended_at IS NOT NULL AND ended_at < @before;

-- name: DeleteRoutineRunsBeyond :exec
-- The newest runs of a Routine are kept; a run still going is never removed.
DELETE FROM routine_runs
WHERE ended_at IS NOT NULL AND id IN (
    SELECT r.id FROM routine_runs r WHERE r.routine_id = @routine_id
    ORDER BY r.started_at DESC, r.id DESC LIMIT -1 OFFSET @keep
);

-- name: UpsertRoutineGrant :exec
INSERT INTO routine_grants (routine_id, integration, label, action, action_name, revision, approval_id, created_at)
VALUES (@routine_id, @integration, @label, @action, @action_name, @revision, @approval_id, @now)
ON CONFLICT (routine_id, integration, label, action) DO UPDATE SET revision = excluded.revision,
    action_name = excluded.action_name, approval_id = excluded.approval_id, created_at = excluded.created_at;

-- name: ListRoutineGrants :many
-- What a Routine is allowed always, at the revision it is at now.
SELECT g.* FROM routine_grants g JOIN routines r ON r.id = g.routine_id AND r.revision = g.revision
WHERE g.routine_id = @routine_id ORDER BY g.integration, g.label, g.action;

-- name: CountRoutineGrants :one
SELECT count(*) FROM routine_grants g JOIN routines r ON r.id = g.routine_id AND r.revision = g.revision
WHERE g.routine_id = @routine_id AND g.integration = @integration AND g.label = @label AND g.action = @action;

-- name: DeleteRoutineGrant :execrows
DELETE FROM routine_grants
WHERE routine_id = @routine_id AND integration = @integration AND label = @label AND action = @action;

-- name: CountPendingApprovalsOfRoutineRun :one
SELECT count(*) FROM approvals WHERE routine_run_id = @routine_run_id AND status = 'pending';

-- name: ListDecidedApprovalsOfRoutineRun :many
-- What the owner answered in a run, and what nobody answered in time, the newest first.
SELECT * FROM approvals WHERE routine_run_id = @routine_run_id AND status <> 'pending'
ORDER BY decided_at DESC, id DESC;

-- name: ExpireApprovalsOfRoutineRun :exec
-- A run that ended leaves no request waiting: what nobody answered is refused.
UPDATE approvals SET status = 'rejected', expired = 1, decided_at = @now
WHERE routine_run_id = @routine_run_id AND status = 'pending';

-- name: ExpireApproval :execrows
-- Nobody answered while the run's call waited.
UPDATE approvals SET status = 'rejected', expired = 1, decided_at = @now WHERE id = @id AND status = 'pending';
