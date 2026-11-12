-- The Lark groups of a Channel (issue #246): a Channel's bot answers in a group it is in once the owner switches the
-- group on, as OpenClaw's Feishu groupPolicy is an allowlist by default. The roster is MemoryOS's channel_groups
-- (ChannelGroupRoster): a group is added when the bot is added to it and by Lark's whole list of the bot's groups,
-- read every ten minutes, which also removes the groups the bot left.

-- +goose Up
-- asked_at is when someone the bot answers last asked there; NULL while nobody has since the group was added. A
-- group's one conversation is started over after four hours without a question (MemoryOS,
-- ChannelProperties.groupConversationIdle).
CREATE TABLE lark_channel_groups (
    channel      TEXT    NOT NULL REFERENCES lark_channels (name) ON DELETE CASCADE,
    lark_chat_id TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 0,
    asked_at     INTEGER,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (channel, lark_chat_id)
) STRICT;

-- A chat that is a Lark group's conversation: the Bot's memory is never in its prompt (OpenClaw leaves MEMORY.md out
-- of a group session's prompt). It is kept on the chat, so a chat a group no longer uses stays one.
ALTER TABLE chats ADD COLUMN lark_group INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE chats DROP COLUMN lark_group;
DROP TABLE lark_channel_groups;
