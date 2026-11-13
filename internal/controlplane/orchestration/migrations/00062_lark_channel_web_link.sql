-- Whether a Channel's replies end with a link to the chat in the web client (issue #305). Only the owner opens the
-- web client, so a Channel for other people goes without it (owner, 2026-11-13); a group's reply never has it
-- (MemoryOS, ChannelInboundService). The Channels that exist keep the link they had.

-- +goose Up
ALTER TABLE lark_channels ADD COLUMN web_link INTEGER NOT NULL DEFAULT 0;

UPDATE lark_channels SET web_link = 1;

-- +goose Down
ALTER TABLE lark_channels DROP COLUMN web_link;
