-- Channels across messaging services (issue #404): a Channel is of one kind, Lark today, and Zalo or WhatsApp later
-- keep theirs in the same tables, as MemoryOS keeps every provider in one set of channel tables (V157, its
-- UNIQUE(provider, app_id)) and Hermes keeps one sessions table with a source column. The Lark tables are renamed in
-- place: SQLite's ALTER TABLE ... RENAME keeps the rows and rewrites the foreign keys that name a table, so nothing
-- is copied. The Channels that exist are Lark's. Their columns take names that are no one service's: a person's id
-- (Lark's open_id), the chat's id at the service (Lark's chat_id, MemoryOS's external_chat_id) and the owner's id.

-- +goose Up
ALTER TABLE lark_channels RENAME TO channels;
ALTER TABLE channels ADD COLUMN kind TEXT NOT NULL DEFAULT 'lark';
ALTER TABLE channels RENAME COLUMN owner_open_id TO owner_id;

-- One app of a kind is one Channel. The unique app_id of 47 stays, so an app id is still one Channel's whatever its
-- kind: dropping it needs the table made again, which waits for a second kind.
CREATE UNIQUE INDEX channels_kind_app ON channels (kind, app_id);

ALTER TABLE lark_channel_people RENAME TO channel_people;
ALTER TABLE channel_people RENAME COLUMN open_id TO person_id;

DROP INDEX lark_channel_chats_channel;
ALTER TABLE lark_channel_chats RENAME TO channel_chats;
ALTER TABLE channel_chats RENAME COLUMN lark_chat_id TO external_chat_id;
ALTER TABLE channel_chats RENAME COLUMN open_id TO person_id;
CREATE INDEX channel_chats_channel ON channel_chats (channel);

ALTER TABLE lark_channel_messages RENAME TO channel_messages;

ALTER TABLE lark_channel_questions RENAME TO channel_questions;

ALTER TABLE lark_channel_groups RENAME TO channel_groups;
ALTER TABLE channel_groups RENAME COLUMN lark_chat_id TO external_chat_id;

ALTER TABLE chats RENAME COLUMN lark_group TO channel_group;

-- +goose Down
ALTER TABLE chats RENAME COLUMN channel_group TO lark_group;

ALTER TABLE channel_groups RENAME COLUMN external_chat_id TO lark_chat_id;
ALTER TABLE channel_groups RENAME TO lark_channel_groups;

ALTER TABLE channel_questions RENAME TO lark_channel_questions;

ALTER TABLE channel_messages RENAME TO lark_channel_messages;

DROP INDEX channel_chats_channel;
ALTER TABLE channel_chats RENAME COLUMN person_id TO open_id;
ALTER TABLE channel_chats RENAME COLUMN external_chat_id TO lark_chat_id;
ALTER TABLE channel_chats RENAME TO lark_channel_chats;
CREATE INDEX lark_channel_chats_channel ON lark_channel_chats (channel);

ALTER TABLE channel_people RENAME COLUMN person_id TO open_id;
ALTER TABLE channel_people RENAME TO lark_channel_people;

DROP INDEX channels_kind_app;
ALTER TABLE channels RENAME COLUMN owner_id TO owner_open_id;
ALTER TABLE channels DROP COLUMN kind;
ALTER TABLE channels RENAME TO lark_channels;
