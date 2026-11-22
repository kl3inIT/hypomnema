-- Videos and their revisions (issue #818).

-- name: InsertVideo :exec
INSERT INTO videos (id, take_id, title, made_by, made_by_name, created_at)
VALUES (@id, @take_id, @title, @made_by, @made_by_name, @now);

-- name: GetVideo :one
SELECT * FROM videos WHERE id = @id;

-- name: GetVideoOfTake :one
SELECT * FROM videos WHERE take_id = @take_id;

-- name: ListVideos :many
SELECT * FROM videos ORDER BY created_at DESC, id;

-- name: DeleteVideo :exec
DELETE FROM videos WHERE id = @id;

-- name: InsertRevision :exec
INSERT INTO video_revisions (video_id, rev, parent, state, lines, voice, captions, aspect, look, pin_provider,
                             pin_voice, requested_by, request_id, request_hash, created_at, updated_at)
VALUES (@video_id, @rev, @parent, 'queued', @lines, @voice, @captions, @aspect, @look, @pin_provider, @pin_voice,
        @requested_by, @request_id, @request_hash, @now, @now);

-- name: GetRevision :one
SELECT * FROM video_revisions WHERE video_id = @video_id AND rev = @rev;

-- name: RevisionByRequest :one
SELECT * FROM video_revisions WHERE requested_by = @requested_by AND request_id = @request_id;

-- name: ListRevisions :many
SELECT * FROM video_revisions WHERE video_id = @video_id ORDER BY rev;

-- name: LastRev :one
SELECT CAST(COALESCE(MAX(rev), 0) AS INTEGER) FROM video_revisions WHERE video_id = @video_id;

-- name: DeleteRevisions :exec
DELETE FROM video_revisions WHERE video_id = @video_id;

-- name: CountQueued :one
SELECT COUNT(*) FROM video_revisions WHERE state = 'queued';

-- name: CountUnfinished :one
SELECT COUNT(*) FROM video_revisions
WHERE video_id = @video_id AND state IN ('queued', 'voicing', 'composing', 'captioning');

-- name: NextQueued :one
SELECT * FROM video_revisions WHERE state = 'queued' ORDER BY created_at, video_id, rev LIMIT 1;

-- The runner's claim: only a revision still queued is taken.
-- name: ClaimRevision :execrows
UPDATE video_revisions SET state = 'voicing', updated_at = @now
WHERE video_id = @video_id AND rev = @rev AND state = 'queued';

-- name: AdvanceRevision :execrows
UPDATE video_revisions SET state = @state, updated_at = @now
WHERE video_id = @video_id AND rev = @rev AND state = @from;

-- name: SetRevisionVoice :exec
UPDATE video_revisions SET provider = @provider, model = @model, used_voice = @used_voice, updated_at = @now
WHERE video_id = @video_id AND rev = @rev;

-- name: FinishRevision :execrows
UPDATE video_revisions SET state = 'review', length_ms = @length_ms, warnings = @warnings, updated_at = @now
WHERE video_id = @video_id AND rev = @rev AND state IN ('composing', 'captioning');

-- name: FailRevision :execrows
UPDATE video_revisions SET state = 'failed', failed_stage = @stage, failed_cause = @cause, updated_at = @now
WHERE video_id = @video_id AND rev = @rev AND state IN ('queued', 'voicing', 'composing', 'captioning');

-- A revision being made when the control plane stopped is failed, at the stage it was in.
-- name: InterruptRevisions :many
UPDATE video_revisions SET failed_stage = state, state = 'failed', failed_cause = 'interrupted', updated_at = @now
WHERE state IN ('voicing', 'composing', 'captioning')
RETURNING video_id, rev;

-- name: UnapproveOthers :exec
UPDATE video_revisions SET state = 'review', updated_at = @now
WHERE video_id = @video_id AND rev != @rev AND state = 'approved';

-- name: ApproveRevision :execrows
UPDATE video_revisions SET state = 'approved', updated_at = @now
WHERE video_id = @video_id AND rev = @rev AND state = 'review';

-- name: RequestChanges :execrows
UPDATE video_revisions SET state = 'changes_requested', note = @note, updated_at = @now
WHERE video_id = @video_id AND rev = @rev AND state IN ('review', 'changes_requested');

-- name: TakesInWork :many
SELECT DISTINCT v.take_id FROM videos v JOIN video_revisions r ON r.video_id = v.id
WHERE r.state IN ('queued', 'voicing', 'composing', 'captioning');

-- The look a make that sends none gets (issue #908, row 15): one row, written by the migration.
-- name: GetVideoSettings :one
SELECT look FROM video_settings WHERE id = 1;

-- name: SetVideoSettings :exec
UPDATE video_settings SET look = @look, updated_at = @now WHERE id = 1;

-- The owner's music library (issue #910, rows 6a and 6d).
-- name: InsertMusic :exec
INSERT INTO video_music (id, name, file, media_type, length_ms, bytes, created_at)
VALUES (@id, @name, @file, @media_type, @length_ms, @bytes, @now);

-- name: GetMusic :one
SELECT * FROM video_music WHERE id = @id;

-- name: ListMusic :many
SELECT * FROM video_music ORDER BY created_at DESC, id;

-- name: DeleteMusic :execrows
DELETE FROM video_music WHERE id = @id;

-- The revisions queued or being made whose look names a track: the track may not be deleted then.
-- name: CountMakingWithMusic :one
SELECT COUNT(*) FROM video_revisions
WHERE state IN ('queued', 'voicing', 'composing', 'captioning')
  AND json_extract(look, '$.music.track') = CAST(@track AS TEXT);
