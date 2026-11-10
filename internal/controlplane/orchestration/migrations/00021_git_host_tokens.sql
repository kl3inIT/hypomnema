-- A git host's token (issue #44). The token is a secret in the vault; a host names it. github.com and gitlab.com
-- get a row here once they have a token, though they need none to be known.

-- +goose Up
ALTER TABLE git_hosts ADD COLUMN token_secret TEXT;

-- +goose Down
ALTER TABLE git_hosts DROP COLUMN token_secret;
