-- name: UpsertLarkChannel :exec
INSERT INTO lark_channels (name, app_id, secret, bot, mode, owner_open_id, enabled, created_at, updated_at)
VALUES (@name, @app_id, @secret, @bot, @mode, @owner_open_id, @enabled, @now, @now)
ON CONFLICT (name) DO UPDATE SET app_id = excluded.app_id, secret = excluded.secret, bot = excluded.bot,
    mode = excluded.mode, owner_open_id = excluded.owner_open_id, enabled = excluded.enabled,
    updated_at = excluded.updated_at;

-- name: GetLarkChannel :one
SELECT * FROM lark_channels WHERE name = @name;

-- name: GetLarkChannelByApp :one
SELECT * FROM lark_channels WHERE app_id = @app_id;

-- name: ListLarkChannels :many
SELECT * FROM lark_channels ORDER BY created_at, name;

-- name: DeleteLarkChannel :execrows
DELETE FROM lark_channels WHERE name = @name;

-- name: CountLarkChannelsOfBot :one
SELECT count(*) FROM lark_channels WHERE bot = @bot;

-- name: FirstEnabledLarkChannel :one
SELECT name FROM lark_channels WHERE enabled = 1 ORDER BY created_at, name LIMIT 1;

-- name: LarkChannelOfRun :one
SELECT c.channel FROM runs r JOIN lark_channel_chats c ON c.chat_id = r.chat_id WHERE r.id = @run_id;

-- name: UpsertLarkChannelPerson :exec
INSERT INTO lark_channel_people (channel, open_id, email, name, created_at)
VALUES (@channel, @open_id, @email, @name, @now)
ON CONFLICT (channel, open_id) DO UPDATE SET email = excluded.email, name = excluded.name;

-- name: GetLarkChannelPerson :one
SELECT * FROM lark_channel_people WHERE channel = @channel AND open_id = @open_id;

-- name: ListLarkChannelPeople :many
SELECT * FROM lark_channel_people WHERE channel = @channel ORDER BY created_at, open_id;

-- name: DeleteLarkChannelPerson :execrows
DELETE FROM lark_channel_people WHERE channel = @channel AND open_id = @open_id;

-- name: InsertLarkChannelChat :exec
INSERT INTO lark_channel_chats (lark_chat_id, channel, open_id, chat_id, sent_seq, created_at)
VALUES (@lark_chat_id, @channel, @open_id, @chat_id, 0, @now);

-- name: GetLarkChannelChat :one
SELECT * FROM lark_channel_chats WHERE channel = @channel AND lark_chat_id = @lark_chat_id;

-- name: ListLarkChannelChats :many
SELECT * FROM lark_channel_chats WHERE channel = @channel ORDER BY created_at, lark_chat_id;

-- name: SetLarkChannelChatSent :exec
UPDATE lark_channel_chats SET sent_seq = @sent_seq WHERE lark_chat_id = @lark_chat_id AND sent_seq < @sent_seq;

-- name: ResetLarkChannelChat :execrows
UPDATE lark_channel_chats SET chat_id = @chat_id, sent_seq = 0
WHERE channel = @channel AND lark_chat_id = @lark_chat_id;

-- name: InsertLarkChannelMessage :execrows
INSERT INTO lark_channel_messages (message_id, channel, created_at) VALUES (@message_id, @channel, @now)
ON CONFLICT DO NOTHING;

-- name: DeleteLarkChannelMessagesBefore :exec
DELETE FROM lark_channel_messages WHERE created_at < @before;

-- name: InsertLarkChannelQuestion :exec
INSERT INTO lark_channel_questions (question_id, channel, card_id, message_id, seq, created_at)
VALUES (@question_id, @channel, @card_id, @message_id, 0, @now);

-- name: GetLarkChannelQuestion :one
SELECT * FROM lark_channel_questions WHERE question_id = @question_id;

-- name: GetLarkChannelQuestionByMessage :one
SELECT * FROM lark_channel_questions WHERE channel = @channel AND message_id = @message_id;

-- name: ListOpenLarkChannelQuestions :many
SELECT * FROM lark_channel_questions WHERE channel = @channel AND closed_at IS NULL ORDER BY created_at, question_id;

-- name: CloseLarkChannelQuestion :exec
UPDATE lark_channel_questions SET closed_at = @now, seq = @seq WHERE question_id = @question_id;
