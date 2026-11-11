-- name: UpsertLarkBot :exec
INSERT INTO lark_bot (id, app_id, secret, mode, owner_open_id, enabled, updated_at)
VALUES (1, @app_id, @secret, @mode, @owner_open_id, @enabled, @now)
ON CONFLICT (id) DO UPDATE SET app_id = excluded.app_id, secret = excluded.secret, mode = excluded.mode,
    owner_open_id = excluded.owner_open_id, enabled = excluded.enabled, updated_at = excluded.updated_at;

-- name: GetLarkBot :one
SELECT * FROM lark_bot WHERE id = 1;

-- name: DeleteLarkBot :exec
DELETE FROM lark_bot WHERE id = 1;

-- name: UpsertLarkBotPerson :exec
INSERT INTO lark_bot_people (open_id, email, name, created_at) VALUES (@open_id, @email, @name, @now)
ON CONFLICT (open_id) DO UPDATE SET email = excluded.email, name = excluded.name;

-- name: GetLarkBotPerson :one
SELECT * FROM lark_bot_people WHERE open_id = @open_id;

-- name: ListLarkBotPeople :many
SELECT * FROM lark_bot_people ORDER BY created_at, open_id;

-- name: DeleteLarkBotPerson :execrows
DELETE FROM lark_bot_people WHERE open_id = @open_id;

-- name: InsertLarkBotChat :exec
INSERT INTO lark_bot_chats (lark_chat_id, open_id, chat_id, sent_seq, created_at)
VALUES (@lark_chat_id, @open_id, @chat_id, 0, @now);

-- name: GetLarkBotChat :one
SELECT * FROM lark_bot_chats WHERE lark_chat_id = @lark_chat_id;

-- name: ListLarkBotChats :many
SELECT * FROM lark_bot_chats ORDER BY created_at, lark_chat_id;

-- name: SetLarkBotChatSent :exec
UPDATE lark_bot_chats SET sent_seq = @sent_seq WHERE lark_chat_id = @lark_chat_id AND sent_seq < @sent_seq;

-- name: InsertLarkBotMessage :execrows
INSERT INTO lark_bot_messages (message_id, created_at) VALUES (@message_id, @now) ON CONFLICT DO NOTHING;

-- name: DeleteLarkBotMessagesBefore :exec
DELETE FROM lark_bot_messages WHERE created_at < @before;
