-- A Claude Code worker runs only through the model gateway, with its Dispatch's key (issue #381): no account's
-- token is sent to a node any more, so the column 00038 added to tell such an account apart goes. An account whose
-- token was sent keeps its secret_ref, and the gateway serves it as it serves every account the vault holds.

-- +goose Up
ALTER TABLE accounts DROP COLUMN sent_to_node;

-- +goose Down
ALTER TABLE accounts ADD COLUMN sent_to_node INTEGER NOT NULL DEFAULT 0 CHECK (sent_to_node IN (0, 1));
