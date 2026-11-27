-- name: InsertArtifactBlob :exec
-- A file's bytes, kept once by their SHA-256.
INSERT INTO artifact_blobs (sha256, size, data) VALUES (@sha256, @size, @data)
ON CONFLICT (sha256) DO NOTHING;

-- name: InsertArtifact :exec
INSERT INTO artifacts (id, chat_id, bot_id, message_seq, revision, created_at, updated_at)
VALUES (@id, @chat_id, @bot, @message_seq, @revision, @now, @now);

-- name: ReviseArtifact :exec
UPDATE artifacts SET revision = @revision, updated_at = @now WHERE id = @id;

-- name: InsertArtifactRevision :exec
INSERT INTO artifact_revisions (artifact_id, revision, title, type, entry, digest, size, created_at)
VALUES (@artifact_id, @revision, @title, @type, @entry, @digest, @size, @now);

-- name: InsertArtifactFile :exec
INSERT INTO artifact_files (artifact_id, revision, path, sha256, size, media_type)
VALUES (@artifact_id, @revision, @path, @sha256, @size, @media_type);

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = @id;

-- name: ListArtifactRevisions :many
-- Newest first.
SELECT * FROM artifact_revisions WHERE artifact_id = @artifact_id ORDER BY revision DESC;

-- name: ListArtifactFiles :many
SELECT * FROM artifact_files WHERE artifact_id = @artifact_id AND revision = @revision ORDER BY path;

-- name: ArtifactFileData :one
SELECT f.path, f.size, f.media_type, b.data
FROM artifact_files f JOIN artifact_blobs b ON b.sha256 = f.sha256
WHERE f.artifact_id = @artifact_id AND f.revision = @revision AND f.path = @path;

-- name: ListArtifacts :many
-- The artifacts with their newest revision, the latest changed first; of one chat when it is named.
SELECT a.id, a.chat_id, a.bot_id, a.message_seq, a.revision, a.created_at, a.updated_at,
       r.title, r.type, r.entry, r.size
FROM artifacts a JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.revision
WHERE (CAST(@chat_id AS TEXT) = '' OR a.chat_id = @chat_id)
  AND (CAST(@bot AS TEXT) = '' OR a.bot_id = @bot)
ORDER BY a.updated_at DESC, a.id
LIMIT @lim;

-- name: DeleteArtifactRevisionsBefore :exec
-- Only the last revisions are kept (OpenMausBot's APP_PAGE_VERSIONS).
DELETE FROM artifact_revisions WHERE artifact_id = @artifact_id AND revision < @revision;

-- name: DeleteUnusedArtifactBlobs :exec
-- The bytes no kept revision names any more.
DELETE FROM artifact_blobs
WHERE sha256 IN (SELECT value FROM json_each(@candidates))
  AND NOT EXISTS (SELECT 1 FROM artifact_files f WHERE f.sha256 = artifact_blobs.sha256);

-- name: ListArtifactBlobsBefore :many
SELECT DISTINCT sha256 FROM artifact_files WHERE artifact_id = @artifact_id AND revision < @revision;

-- name: SetArtifactMessage :exec
-- The artifact's message says its newest title.
UPDATE chat_messages SET text = @text WHERE seq = @seq;

-- name: ArtifactsBeforeReply :many
-- The artifacts made or changed in a chat between a reply and the reply before it that had text: what the reply
-- follows (issue #402, for the Lark relay).
SELECT a.id, r.title, r.type
FROM artifacts a JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.revision
WHERE a.chat_id = @chat_id
  AND a.updated_at <= (SELECT m.created_at FROM chat_messages m WHERE m.seq = @reply_seq)
  AND a.updated_at > coalesce((
      SELECT max(p.created_at) FROM chat_messages p
      WHERE p.chat_id = @chat_id AND p.role = 'coordinator' AND p.seq < @reply_seq AND trim(p.text) != ''
  ), 0)
ORDER BY a.updated_at, a.id;

-- name: ArtifactRevisionData :many
-- Every file of a revision with its bytes, for its archive.
SELECT f.path, f.size, f.media_type, b.data
FROM artifact_files f JOIN artifact_blobs b ON b.sha256 = f.sha256
WHERE f.artifact_id = @artifact_id AND f.revision = @revision
ORDER BY f.path;

-- View-only links to an artifact (issue #745).

-- name: InsertArtifactShare :exec
-- Only the token's hash and its sealed copy are kept (issue #996).
INSERT INTO artifact_shares (id, artifact_id, token_hash, secret_version_id, label, created_at)
VALUES (@id, @artifact_id, @token_hash, @secret_version_id, @label, @now);

-- name: ListArtifactShares :many
-- The links that work, newest first.
SELECT * FROM artifact_shares WHERE artifact_id = @artifact_id AND revoked_at IS NULL ORDER BY created_at DESC, id;

-- name: GetLiveArtifactShare :one
SELECT * FROM artifact_shares WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeArtifactShare :execrows
-- A revoked link is kept, without its sealed copy, which the caller deletes.
UPDATE artifact_shares SET revoked_at = @now, revoke_reason = @reason, secret_version_id = NULL
WHERE id = @id AND revoked_at IS NULL;

-- name: SharedArtifact :one
-- What a working link shows: its artifact at the newest revision, with the type of the file shown.
SELECT s.id, s.artifact_id, a.revision, a.updated_at, r.title, r.type, r.entry, f.media_type
FROM artifact_shares s
JOIN artifacts a ON a.id = s.artifact_id
JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.revision
JOIN artifact_files f ON f.artifact_id = a.id AND f.revision = a.revision AND f.path = r.entry
WHERE s.revoked_at IS NULL AND s.token_hash = @token_hash;

-- name: CountArtifactShareView :exec
-- The openings counted since the last write (issue #996), written together; its last use is written at most once an
-- hour, as every credential's. A revoked link's are dropped.
UPDATE artifact_shares SET views = views + CAST(@views AS INTEGER),
    last_viewed_at = CASE WHEN last_viewed_at IS NULL OR last_viewed_at <= CAST(@before AS INTEGER)
                          THEN CAST(@now AS INTEGER) ELSE last_viewed_at END
WHERE id = @id AND revoked_at IS NULL;

-- name: ListAllArtifactShares :many
-- Every link that works, newest first, with its artifact's newest title and type, its Bot and its chat (issue #765).
SELECT s.id, s.artifact_id, s.token_hash, s.secret_version_id, s.label, s.created_at, s.views, s.last_viewed_at,
       a.chat_id, a.bot_id, coalesce(b.name, '') AS bot_name, r.title, r.type
FROM artifact_shares s
JOIN artifacts a ON a.id = s.artifact_id
JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.revision
LEFT JOIN bots b ON b.id = a.bot_id
WHERE s.revoked_at IS NULL
ORDER BY s.created_at DESC, s.id;

-- name: ListShareSecretsOfChat :many
-- The sealed copies of a chat's links, deleted with the chat as its artifacts and their links are.
SELECT s.secret_version_id FROM artifact_shares s JOIN artifacts a ON a.id = s.artifact_id
WHERE a.chat_id = @chat_id AND s.secret_version_id IS NOT NULL;

-- name: ListShareLabelsBySecret :many
-- The links whose sealed token is the secret, so the vault refuses to delete it (issue #996), as a gateway key's.
SELECT s.id, s.label FROM artifact_shares s
JOIN secret_versions v ON v.id = s.secret_version_id
JOIN secrets ON secrets.id = v.secret_id
WHERE secrets.name = @secret_name
ORDER BY s.id;
