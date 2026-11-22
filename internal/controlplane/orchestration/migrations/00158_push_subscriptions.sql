-- Web Push (issue #1047). The owner alone uses the app (owner, 2026-11-21): a subscription is one row per browser
-- endpoint with its keys and the device's label, and no user column, as the owner's Second Bite keeps one row per
-- endpoint (docs/specs/notification.md). Subscribing again from the same browser refreshes its keys, as Coder's
-- upsert on its endpoint (coderd/database/queries/notifications.sql:198-209).
--
-- push_outbox is what waits to be pushed: a row written in the same transaction as the item that waits on the owner
-- (a question for him, a pending Approval, a Connection request, an Inbox to-do that needs him), read by the sender
-- after the commit and deleted once sent, as the owner's Second Bite sends through an outbox after commit
-- (server/internal/notification/push_sender.go). One row per item: the item's push names it, so a second write of
-- the same item adds nothing.

-- +goose Up
CREATE TABLE push_subscriptions (
    id         TEXT    NOT NULL PRIMARY KEY,
    endpoint   TEXT    NOT NULL UNIQUE CHECK (length(endpoint) BETWEEN 1 AND 2000),
    p256dh     TEXT    NOT NULL CHECK (length(p256dh) BETWEEN 1 AND 200),
    auth       TEXT    NOT NULL CHECK (length(auth) BETWEEN 1 AND 100),
    label      TEXT    NOT NULL DEFAULT '' CHECK (length(label) <= 100),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE push_outbox (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL CHECK (kind IN ('question', 'approval', 'connection_request', 'todo')),
    item_id    TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (kind, item_id)
) STRICT;

-- +goose Down
DROP TABLE push_outbox;
DROP TABLE push_subscriptions;
