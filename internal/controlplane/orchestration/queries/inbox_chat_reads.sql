-- Which of the owner's chats the secretary reads (issue #1270; migration inbox_chat_reads): his choice per chat of
-- a Connection. A chat without a row takes its kind's default (inboxstore.DefaultReads).

-- name: SetInboxChatRead :one
-- The owner's choice for one chat, replacing the one he made before.
INSERT INTO inbox_chat_reads (integration, label, chat_id, reads, set_at)
VALUES (@integration, @label, @chat_id, @reads, @now)
ON CONFLICT (integration, label, chat_id) DO UPDATE SET reads = excluded.reads, set_at = excluded.set_at
RETURNING *;

-- name: AddInboxChatRead :execrows
-- The owner's choice for one chat when he has made none yet: the one-time move of a Bot's dropChats (issue #1270)
-- never undoes a choice made since, so running it again changes nothing.
INSERT INTO inbox_chat_reads (integration, label, chat_id, reads, set_at)
VALUES (@integration, @label, @chat_id, @reads, @now)
ON CONFLICT (integration, label, chat_id) DO NOTHING;

-- name: ListInboxChatReads :many
-- Every choice the owner made, of one Connection when integration is set.
SELECT * FROM inbox_chat_reads
WHERE (CAST(@integration AS TEXT) = '' OR (integration = CAST(@integration AS TEXT) AND label = CAST(@label AS TEXT)))
ORDER BY integration, label, chat_id;
