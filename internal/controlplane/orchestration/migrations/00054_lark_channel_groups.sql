-- The Lark groups of a Channel (issue #246): a Channel's bot answers in a group it is in once the owner switches the
-- group on, as OpenClaw's Feishu groupPolicy is an allowlist by default. The roster is MemoryOS's channel_groups
-- (ChannelGroupRoster): a group is added when the bot is added to it and by Lark's whole list of the bot's groups,
-- read every ten minutes, which also removes the groups the bot left.

-- +goose Up
-- asked_at is when someone the bot answered last asked there; NULL while nobody has. A group's one conversation is
-- started over after four hours without a question (MemoryOS, ChannelProperties.groupConversationIdle).
CREATE TABLE lark_channel_groups (
    channel      TEXT    NOT NULL REFERENCES lark_channels (name) ON DELETE CASCADE,
    lark_chat_id TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 0,
    asked_at     INTEGER,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (channel, lark_chat_id)
) STRICT;

-- A Lark chat that is a group: its chat with the Bot is the whole group's, and the Bot's memory is not in its prompt
-- (OpenClaw leaves MEMORY.md out of a group session's prompt).
ALTER TABLE lark_channel_chats ADD COLUMN is_group INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE lark_channel_chats DROP COLUMN is_group;
DROP TABLE lark_channel_groups;
