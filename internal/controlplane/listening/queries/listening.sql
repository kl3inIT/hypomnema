-- Social listening's sources as the owner sets them and as they last read (issue #1290).

-- name: ListListeningSources :many
SELECT * FROM listening_sources ORDER BY source;

-- name: SetListeningSourceOff :exec
INSERT INTO listening_sources (source, off, updated_at) VALUES (@source, @off, @now)
ON CONFLICT (source) DO UPDATE SET off = excluded.off, updated_at = excluded.updated_at;

-- name: ListeningSourceIsOff :one
SELECT COUNT(*) FROM listening_sources WHERE source = @source AND off = 1;

-- Each reading moves the last result to the one before it (Agent-Reach's previous check, manage.py:189-206).
-- name: RecordListeningRead :exec
INSERT INTO listening_sources (source, last_read_at, last_result, last_code, updated_at)
VALUES (@source, CAST(@now AS INTEGER), @result, @code, CAST(@now AS INTEGER))
ON CONFLICT (source) DO UPDATE SET previous_result = listening_sources.last_result,
    last_read_at = excluded.last_read_at, last_result = excluded.last_result, last_code = excluded.last_code,
    updated_at = excluded.updated_at;

-- name: RecordListeningSuccess :exec
INSERT INTO listening_sources (source, last_success_at, updated_at)
VALUES (@source, CAST(@now AS INTEGER), CAST(@now AS INTEGER))
ON CONFLICT (source) DO UPDATE SET last_success_at = excluded.last_success_at, updated_at = excluded.updated_at;

-- A reading that read items clears the flag that the owner was told it broke, as only a refresh that returns an
-- access token clears postiz's refreshNeeded (refresh.integration.service.ts:20-54); a clean zero does not.
-- name: ClearListeningTold :exec
UPDATE listening_sources SET told = 0, updated_at = @now WHERE source = @source;

-- name: RecordListeningError :exec
INSERT INTO listening_sources (source, last_error_at, last_error_code, last_error_detail, last_error_backend,
    updated_at)
VALUES (@source, CAST(@now AS INTEGER), @code, @detail, @backend, CAST(@now AS INTEGER))
ON CONFLICT (source) DO UPDATE SET last_error_at = excluded.last_error_at,
    last_error_code = excluded.last_error_code, last_error_detail = excluded.last_error_detail,
    last_error_backend = excluded.last_error_backend, updated_at = excluded.updated_at;

-- Only the reading that flags it tells the owner (postiz integration.repository.ts:603-616, refreshNeeded set where
-- it was false; refresh.integration.service.ts:148-166).
-- name: FlagListeningTold :execrows
UPDATE listening_sources SET told = 1, updated_at = @now WHERE source = @source AND told = 0;

-- name: ListListeningBackends :many
SELECT * FROM listening_backends ORDER BY source, backend;

-- name: SetListeningBackendPosition :exec
INSERT INTO listening_backends (source, backend, position, updated_at) VALUES (@source, @backend, @position, @now)
ON CONFLICT (source, backend) DO UPDATE SET position = excluded.position, updated_at = excluded.updated_at;

-- name: RecordListeningBackendOK :exec
INSERT INTO listening_backends (source, backend, last_ok_at, updated_at)
VALUES (@source, @backend, CAST(@now AS INTEGER), CAST(@now AS INTEGER))
ON CONFLICT (source, backend) DO UPDATE SET last_ok_at = excluded.last_ok_at, updated_at = excluded.updated_at;

-- name: RecordListeningBackendError :exec
INSERT INTO listening_backends (source, backend, last_error_at, last_code, last_detail, updated_at)
VALUES (@source, @backend, CAST(@now AS INTEGER), @code, @detail, CAST(@now AS INTEGER))
ON CONFLICT (source, backend) DO UPDATE SET last_error_at = excluded.last_error_at, last_code = excluded.last_code,
    last_detail = excluded.last_detail, updated_at = excluded.updated_at;

-- name: AddListeningItems :exec
INSERT INTO listening_readings (source, day, items) VALUES (@source, @day, @items)
ON CONFLICT (source, day) DO UPDATE SET items = items + excluded.items;

-- name: PruneListeningReadings :exec
DELETE FROM listening_readings WHERE day < @before;

-- name: ListListeningReadings :many
SELECT * FROM listening_readings WHERE day >= @since ORDER BY source, day DESC;

-- A source's consent card agreed to (issue #1291): kept once, the first time, and the source switched on.
-- name: ConsentListeningSource :exec
INSERT INTO listening_sources (source, off, consented_at, updated_at) VALUES (@source, 0, CAST(@now AS INTEGER),
    CAST(@now AS INTEGER))
ON CONFLICT (source) DO UPDATE SET off = 0,
    consented_at = COALESCE(listening_sources.consented_at, excluded.consented_at), updated_at = excluded.updated_at;

-- name: ListListeningAccounts :many
SELECT * FROM listening_accounts ORDER BY source;

-- name: GetListeningAccount :one
SELECT * FROM listening_accounts WHERE source = @source;

-- A sign-in, new or again, replaces the account and clears what the last one met.
-- name: PutListeningAccount :exec
INSERT INTO listening_accounts (source, label, secret, state, cause, signed_in_at, checked_at, updated_at)
VALUES (@source, @label, @secret, 'signed_in', '', CAST(@now AS INTEGER), CAST(@now AS INTEGER),
    CAST(@now AS INTEGER))
ON CONFLICT (source) DO UPDATE SET label = excluded.label, secret = excluded.secret, state = 'signed_in', cause = '',
    signed_in_at = excluded.signed_in_at, checked_at = excluded.checked_at, updated_at = excluded.updated_at;

-- A real call that asked for a sign-in or refused the account marks it, once: the row changes only when its state
-- does, so only that call tells the owner (postiz's refreshNeeded, set where it was not, integration.repository.ts:
-- 603-616). A refusal stays until a new sign-in: a need_login never replaces it.
-- name: MarkListeningAccount :execrows
UPDATE listening_accounts SET state = @state, cause = @cause, checked_at = CAST(@now AS INTEGER),
    updated_at = CAST(@now AS INTEGER)
WHERE source = @source AND state <> @state AND state <> 'refused';

-- A throttling page stops the source's site until then, a later stop never shortened by an earlier one; nothing but
-- the time ends it, neither a sign-in nor a search.
-- name: StopListeningSource :exec
INSERT INTO listening_sources (source, stopped_until, stop_cause, updated_at)
VALUES (@source, CAST(@until AS INTEGER), @cause, CAST(@now AS INTEGER))
ON CONFLICT (source) DO UPDATE SET
    stopped_until = MAX(COALESCE(listening_sources.stopped_until, 0), excluded.stopped_until),
    stop_cause = excluded.stop_cause, updated_at = excluded.updated_at;

-- name: ListeningSourceStop :one
SELECT stopped_until, stop_cause FROM listening_sources WHERE source = @source;

-- name: ConfirmListeningAccount :exec
UPDATE listening_accounts SET checked_at = CAST(@now AS INTEGER), updated_at = CAST(@now AS INTEGER)
WHERE source = @source AND state = 'signed_in';

-- name: DeleteListeningAccount :execrows
DELETE FROM listening_accounts WHERE source = @source;
