-- Web Push (issue #1047; migration push_subscriptions): the owner's browsers and what waits to be pushed to them.

-- name: UpsertPushSubscription :one
-- A browser subscribing again keeps its row and gets its new keys, as Coder's InsertWebpushSubscription
-- (coderd/database/queries/notifications.sql:198-209), and its new label when it gives one: the service worker
-- renewing a subscription gives none.
INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, label, created_at, updated_at)
VALUES (@id, @endpoint, @p256dh, @auth, @label, @now, @now)
ON CONFLICT (endpoint) DO UPDATE
    SET p256dh = excluded.p256dh, auth = excluded.auth, updated_at = excluded.updated_at,
        label = CASE WHEN excluded.label = '' THEN push_subscriptions.label ELSE excluded.label END
RETURNING *;

-- name: ListPushSubscriptions :many
SELECT * FROM push_subscriptions ORDER BY created_at, id;

-- name: GetPushSubscription :one
SELECT * FROM push_subscriptions WHERE id = @id;

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions WHERE id = @id;

-- name: DeletePushSubscriptionByEndpoint :execrows
-- The push service said the endpoint is gone (404 or 410).
DELETE FROM push_subscriptions WHERE endpoint = @endpoint;

-- name: DeleteAllPushSubscriptions :execrows
-- The VAPID keys were made again: every subscription was made with the old public key.
DELETE FROM push_subscriptions;

-- name: InsertPushOutbox :exec
-- In the transaction of the item that waits on the owner; an item already queued adds nothing.
INSERT INTO push_outbox (kind, item_id, created_at) VALUES (@kind, @item_id, @now)
ON CONFLICT (kind, item_id) DO NOTHING;

-- name: ListPushOutbox :many
SELECT * FROM push_outbox ORDER BY seq LIMIT @lim;

-- name: DeletePushOutbox :exec
DELETE FROM push_outbox WHERE seq = @seq;

-- name: TodoNeedsYou :one
-- Whether one to-do needs the owner now: the needs_you predicate of ListTodos and CountTodos, for one item.
SELECT count(*) FROM todos t
WHERE t.id = @id
  AND t.state NOT IN ('done', 'dropped', 'duplicate')
  AND NOT (t.state = 'proposed' AND t.revision = 1 AND t.created_at <= CAST(@stale_before AS INTEGER))
  AND (t.snoozed_until IS NULL OR t.snoozed_until <= CAST(@now AS INTEGER))
  AND (t.state = 'proposed'
    OR (t.state = 'review' AND EXISTS (SELECT 1 FROM approvals a WHERE a.id = t.approval_id AND a.status = 'pending'))
    OR (t.assignee_kind = 'me' AND t.draft <> '')
    OR (t.assignee_kind = 'me' AND t.overdue_at IS NOT NULL AND t.overdue_at <= CAST(@now AS INTEGER)));
