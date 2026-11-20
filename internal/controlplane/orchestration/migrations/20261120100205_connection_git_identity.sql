-- The account a git host Connection's token belongs to, as a worker's commits name it (issue #991): its login and
-- its noreply or commit address, read once from the host's /user with the token and kept with the Connection, as
-- Paperclip keeps a GitHub grant's login and id (server/src/services/git-credentials.ts:53). git_secret_version_id
-- is the id of the vault secret version whose token it was read with, never reused: a token replaced, by the
-- Connection, on the secret itself, or after a disconnect, is another version and is read again, as it may be
-- another account's. NULL until read.

-- +goose Up
ALTER TABLE connections ADD COLUMN git_name TEXT;
ALTER TABLE connections ADD COLUMN git_email TEXT;
ALTER TABLE connections ADD COLUMN git_secret_version_id TEXT;

-- +goose Down
ALTER TABLE connections DROP COLUMN git_secret_version_id;
ALTER TABLE connections DROP COLUMN git_email;
ALTER TABLE connections DROP COLUMN git_name;
