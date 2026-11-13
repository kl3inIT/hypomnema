-- An account logged in through the control plane is the same login only when its vendor id and its e-mail both
-- match (issue #326). Codex's vendor id is the ChatGPT workspace's id, which every member of a Business (Team)
-- workspace shares: keyed on it alone, a second member's login replaced the first's tokens. omp keys a Codex
-- credential by e-mail and workspace (packages/ai/src/auth/sqlite-credential-store.ts, org-scoped-identity in
-- packages/catalog/src/compat/rules/auth/openai-codex.kdl); CLIProxyAPI names its file by both
-- (internal/auth/codex/filename.go).

-- +goose Up
DROP INDEX accounts_external;
CREATE UNIQUE INDEX accounts_external ON accounts (runtime, external_id, lower(coalesce(email, '')))
    WHERE external_id IS NOT NULL;

-- +goose Down
DROP INDEX accounts_external;
CREATE UNIQUE INDEX accounts_external ON accounts (runtime, external_id) WHERE external_id IS NOT NULL;
