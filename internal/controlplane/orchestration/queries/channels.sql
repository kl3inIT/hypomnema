-- name: UpsertChannel :exec
INSERT INTO channels (name, kind, app_id, secret, bot_id, mode, owner_id, enabled, web_link, created_at,
    updated_at)
VALUES (@name, @kind, @app_id, @secret, @bot, @mode, @owner_id, @enabled, @web_link, @now, @now)
ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, app_id = excluded.app_id, secret = excluded.secret,
    bot_id = excluded.bot_id, mode = excluded.mode, owner_id = excluded.owner_id, enabled = excluded.enabled,
    web_link = excluded.web_link, updated_at = excluded.updated_at;

-- name: GetChannel :one
SELECT * FROM channels WHERE name = @name;

-- name: GetChannelByApp :one
SELECT * FROM channels WHERE kind = @kind AND app_id = @app_id;

-- name: ListChannels :many
SELECT * FROM channels ORDER BY created_at, name;

-- name: DeleteChannel :execrows
DELETE FROM channels WHERE name = @name;

-- name: CountChannelsOfBot :one
SELECT count(*) FROM channels WHERE bot_id = @bot;

-- name: FirstEnabledChannel :one
SELECT name FROM channels WHERE enabled = 1 ORDER BY created_at, name LIMIT 1;

-- name: ChannelOfRun :one
SELECT c.channel FROM runs r JOIN channel_chats c ON c.chat_id = r.chat_id WHERE r.id = @run_id;

-- name: UpsertChannelPerson :exec
INSERT INTO channel_people (channel, person_id, email, name, created_at)
VALUES (@channel, @person_id, @email, @name, @now)
ON CONFLICT (channel, person_id) DO UPDATE SET email = excluded.email, name = excluded.name;

-- name: GetChannelPerson :one
SELECT * FROM channel_people WHERE channel = @channel AND person_id = @person_id;

-- name: ListChannelPeople :many
SELECT * FROM channel_people WHERE channel = @channel ORDER BY created_at, person_id;

-- name: DeleteChannelPerson :execrows
DELETE FROM channel_people WHERE channel = @channel AND person_id = @person_id;

-- name: InsertChannelChat :exec
INSERT INTO channel_chats (external_chat_id, channel, person_id, chat_id, sent_seq, created_at)
VALUES (@external_chat_id, @channel, @person_id, @chat_id, 0, @now);

-- name: GetChannelChat :one
SELECT * FROM channel_chats WHERE channel = @channel AND external_chat_id = @external_chat_id;

-- name: ListChannelChats :many
SELECT * FROM channel_chats WHERE channel = @channel ORDER BY created_at, external_chat_id;

-- name: SetChannelChatSent :exec
UPDATE channel_chats SET sent_seq = @sent_seq WHERE external_chat_id = @external_chat_id AND sent_seq < @sent_seq;

-- name: RenameChannelChat :execrows
UPDATE channel_chats SET external_chat_id = @new_external_chat_id
WHERE channel = @channel AND external_chat_id = @external_chat_id;

-- name: ResetChannelChat :execrows
UPDATE channel_chats SET chat_id = @chat_id, sent_seq = 0
WHERE channel = @channel AND external_chat_id = @external_chat_id;

-- name: DeleteChannelChatOfChat :exec
-- The Lark chat a deleted chat answered (issue #484): its next message starts a new chat.
DELETE FROM channel_chats WHERE chat_id = @chat_id;

-- name: InsertChannelMessage :execrows
INSERT INTO channel_messages (message_id, channel, created_at) VALUES (@message_id, @channel, @now)
ON CONFLICT DO NOTHING;

-- name: DeleteChannelMessagesBefore :exec
DELETE FROM channel_messages WHERE created_at < @before;

-- name: InsertChannelQuestion :exec
INSERT INTO channel_questions (question_id, channel, card_id, message_id, seq, created_at)
VALUES (@question_id, @channel, @card_id, @message_id, 0, @now);

-- name: GetChannelQuestion :one
SELECT * FROM channel_questions WHERE question_id = @question_id;

-- name: GetChannelQuestionByMessage :one
SELECT * FROM channel_questions WHERE channel = @channel AND message_id = @message_id;

-- name: ListOpenChannelQuestions :many
SELECT * FROM channel_questions WHERE channel = @channel AND closed_at IS NULL ORDER BY created_at, question_id;

-- name: CloseChannelQuestion :exec
UPDATE channel_questions SET closed_at = @now, seq = @seq WHERE question_id = @question_id;

-- name: IsChannelGroupChat :one
SELECT channel_group FROM chats WHERE id = @chat_id;

-- name: MarkChannelGroupChat :exec
UPDATE chats SET channel_group = 1 WHERE id = @chat_id;

-- name: UpsertChannelGroup :exec
INSERT INTO channel_groups (channel, external_chat_id, name, enabled, created_at)
VALUES (@channel, @external_chat_id, @name, 0, @now)
ON CONFLICT (channel, external_chat_id) DO UPDATE SET
    name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE channel_groups.name END;

-- name: GetChannelGroup :one
SELECT * FROM channel_groups WHERE channel = @channel AND external_chat_id = @external_chat_id;

-- name: ListChannelGroups :many
SELECT * FROM channel_groups WHERE channel = @channel
ORDER BY asked_at IS NULL, asked_at DESC, name, external_chat_id;

-- name: SetChannelGroupEnabled :execrows
UPDATE channel_groups SET enabled = @enabled WHERE channel = @channel AND external_chat_id = @external_chat_id;

-- name: SetChannelGroupAsked :exec
UPDATE channel_groups SET asked_at = @now WHERE channel = @channel AND external_chat_id = @external_chat_id;

-- name: DeleteChannelGroup :execrows
DELETE FROM channel_groups WHERE channel = @channel AND external_chat_id = @external_chat_id;
