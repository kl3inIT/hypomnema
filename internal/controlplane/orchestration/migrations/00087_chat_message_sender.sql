-- Each person message of a chat records who wrote it (issue #468): the owner, or a person of a Channel with their id
-- at the service and their name, as OpenMausBot keeps Message.sender {name, id} (server/store.ts). sender_id is
-- NULL for the owner, the one who writes on the web, from the CLI and as the Channel's owner at Lark; any other
-- value is someone else, '' when who it was is not known. What decides whether a Bot acts for the owner (an owner
-- turn) reads it, so a row it cannot vouch for is never the owner's.
--
-- Before this migration a Lark message from someone else was an owner row with a label in its text. The rows of a
-- direct chat with a person who is not the Channel's owner become that person's; the rows of a group's chat
-- become someone's not known, the owner's included, since the group's one chat mixed everyone's; the owner's
-- next message there is his again.

-- +goose Up
ALTER TABLE chat_messages ADD COLUMN sender_id TEXT;
ALTER TABLE chat_messages ADD COLUMN sender_name TEXT;
ALTER TABLE chat_queue ADD COLUMN sender_id TEXT;
ALTER TABLE chat_queue ADD COLUMN sender_name TEXT;

UPDATE chat_messages
SET sender_id = (
        SELECT cc.person_id FROM channel_chats cc JOIN channels c ON c.name = cc.channel
        WHERE cc.chat_id = chat_messages.chat_id
    ),
    sender_name = (
        SELECT p.name FROM channel_chats cc JOIN channel_people p ON p.channel = cc.channel AND p.person_id = cc.person_id
        WHERE cc.chat_id = chat_messages.chat_id
    )
WHERE role = 'owner'
  AND EXISTS (
        SELECT 1 FROM channel_chats cc JOIN channels c ON c.name = cc.channel
        JOIN chats ch ON ch.id = cc.chat_id
        WHERE cc.chat_id = chat_messages.chat_id AND ch.channel_group = 0 AND cc.person_id <> c.owner_id
    );

UPDATE chat_messages SET sender_id = ''
WHERE role = 'owner' AND sender_id IS NULL
  AND chat_id IN (SELECT id FROM chats WHERE channel_group = 1);

UPDATE chat_queue SET sender_id = ''
WHERE chat_id IN (SELECT id FROM chats WHERE channel_group = 1)
   OR chat_id IN (
        SELECT cc.chat_id FROM channel_chats cc JOIN channels c ON c.name = cc.channel WHERE cc.person_id <> c.owner_id
    );

-- +goose Down
ALTER TABLE chat_queue DROP COLUMN sender_name;
ALTER TABLE chat_queue DROP COLUMN sender_id;
ALTER TABLE chat_messages DROP COLUMN sender_name;
ALTER TABLE chat_messages DROP COLUMN sender_id;
