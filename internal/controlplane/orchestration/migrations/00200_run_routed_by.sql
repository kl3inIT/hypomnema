-- +goose Up
-- Why a Run went to its chat (issue #1334), as OpenClaw's route says what it matched by (src/routing/resolve-route.ts,
-- matchedBy): chat, the chat its creator named; own_chat, the Bot chat that created it; repo_owner, the main chat of
-- the Bot that owns its repository; default, the main Bot's main chat. NULL for a Run made before, or without a chat.
ALTER TABLE runs ADD COLUMN routed_by TEXT CHECK (routed_by IN ('chat', 'own_chat', 'repo_owner', 'default'));

-- +goose Down
ALTER TABLE runs DROP COLUMN routed_by;
