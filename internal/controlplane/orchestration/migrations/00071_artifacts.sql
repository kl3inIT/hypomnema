-- Artifacts (issue #402): what a Bot hands the owner to look at in its chat, an HTML page or folder, a PDF, an
-- image or any other file, after OpenMausBot's chat pages (server/app-docs.ts, server/chat-apps.ts:160-207): each
-- revision is kept by its files, and the files by their SHA-256, so that the same bytes are kept once; the chat
-- shows it as one message of kind 'artifact', which a replacement updates in place. Only the last revisions are
-- kept, as OpenMausBot keeps ten versions of a page (APP_PAGE_VERSIONS). The files are in the database, as the
-- skills' archives are: one database is one backup.

-- +goose Up
CREATE TABLE artifact_blobs (
    sha256 TEXT    NOT NULL PRIMARY KEY CHECK (length(sha256) = 64),
    size   INTEGER NOT NULL CHECK (size >= 0),
    data   BLOB    NOT NULL
) STRICT;

CREATE TABLE artifacts (
    id          TEXT    NOT NULL PRIMARY KEY,
    chat_id     TEXT    NOT NULL REFERENCES chats (id),
    -- The Bot that made it, in its chat; a Routine's script of that Bot replaces it.
    bot         TEXT    NOT NULL,
    -- The chat's message of kind 'artifact' that shows it.
    message_seq INTEGER NOT NULL,
    -- The newest revision.
    revision    INTEGER NOT NULL CHECK (revision > 0),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX artifacts_chat ON artifacts (chat_id, updated_at);
CREATE UNIQUE INDEX artifacts_message ON artifacts (message_seq);

CREATE TABLE artifact_revisions (
    artifact_id TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    revision    INTEGER NOT NULL CHECK (revision > 0),
    title       TEXT    NOT NULL CHECK (length(title) > 0),
    -- What its viewer is, found from the bytes and the name (internal/controlplane/artifactpack).
    type        TEXT    NOT NULL CHECK (type IN ('html', 'pdf', 'image', 'file')),
    -- The file shown: index.html of a folder, or the one file.
    entry       TEXT    NOT NULL,
    -- SHA-256 of the files' paths and digests: the same files again make no new revision.
    digest      TEXT    NOT NULL CHECK (length(digest) = 64),
    size        INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (artifact_id, revision)
) STRICT;

CREATE TABLE artifact_files (
    artifact_id TEXT    NOT NULL,
    revision    INTEGER NOT NULL,
    path        TEXT    NOT NULL,
    sha256      TEXT    NOT NULL REFERENCES artifact_blobs (sha256),
    size        INTEGER NOT NULL,
    media_type  TEXT    NOT NULL,
    PRIMARY KEY (artifact_id, revision, path),
    FOREIGN KEY (artifact_id, revision) REFERENCES artifact_revisions (artifact_id, revision) ON DELETE CASCADE
) STRICT;

CREATE INDEX artifact_files_blob ON artifact_files (sha256);

-- A chat message's kind takes 'artifact'. SQLite changes a CHECK only by rebuilding; the column alone is made
-- again, as nothing else of the schema names it.
ALTER TABLE chat_messages ADD COLUMN kind_new TEXT NOT NULL DEFAULT ''
    CHECK (kind_new IN ('', 'wake', 'failed', 'stopped', 'approval', 'artifact'));
UPDATE chat_messages SET kind_new = kind;
ALTER TABLE chat_messages DROP COLUMN kind;
ALTER TABLE chat_messages RENAME COLUMN kind_new TO kind;

-- +goose Down
DELETE FROM chat_messages WHERE kind = 'artifact';
ALTER TABLE chat_messages ADD COLUMN kind_old TEXT NOT NULL DEFAULT ''
    CHECK (kind_old IN ('', 'wake', 'failed', 'stopped', 'approval'));
UPDATE chat_messages SET kind_old = kind;
ALTER TABLE chat_messages DROP COLUMN kind;
ALTER TABLE chat_messages RENAME COLUMN kind_old TO kind;
DROP INDEX artifact_files_blob;
DROP TABLE artifact_files;
DROP TABLE artifact_revisions;
DROP INDEX artifacts_message;
DROP INDEX artifacts_chat;
DROP TABLE artifacts;
DROP TABLE artifact_blobs;
