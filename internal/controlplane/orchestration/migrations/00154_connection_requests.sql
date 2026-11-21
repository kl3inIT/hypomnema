-- A Bot's request for a Connection it was not given (issue #471, part D, the grant card): its pi in the Bot's main
-- chat asks with request_connection, the owner answers on a card with Always for this Bot or Decline. A record of its
-- own, not an Approval, whose table and scopes stay as they are (#471 review P1-3). One pending request per (Bot,
-- chat, Connection), as OpenMausBot finds the card a repeated request already made (server/host/connector-cards.ts,
-- the existing message of the same resumeKey and slug). turn is the chat's turn it was asked in
-- (orchestration.ChatTurn): a later ask in the same turn, or in a turn the owner did not start, gets this request's
-- state and makes no card (coordinator, 2026-11-21; OpenMausBot's resumeKey, t3code's clientRequestId,
-- apps/server/src/peer/PeerLinkRequests.ts). stale is an Always whose checks failed: it granted nothing. A request
-- goes with its chat (orchestration deleteChatTx), and so with its Bot's chats.

-- +goose Up
CREATE TABLE connection_requests (
    id          TEXT    NOT NULL PRIMARY KEY,
    bot_id      TEXT    NOT NULL REFERENCES bots (id),
    chat_id     TEXT    NOT NULL REFERENCES chats (id),
    integration TEXT    NOT NULL CHECK (length(integration) > 0),
    label       TEXT    NOT NULL CHECK (length(label) > 0),
    reason      TEXT    NOT NULL,
    turn        TEXT    NOT NULL,
    state       TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'granted', 'declined', 'stale')),
    created_at  INTEGER NOT NULL,
    decided_at  INTEGER
) STRICT;

CREATE UNIQUE INDEX connection_requests_pending ON connection_requests (bot_id, chat_id, integration, label)
    WHERE state = 'pending';

CREATE INDEX connection_requests_chat ON connection_requests (chat_id, created_at);

-- +goose Down
DROP INDEX connection_requests_chat;
DROP INDEX connection_requests_pending;
DROP TABLE connection_requests;
