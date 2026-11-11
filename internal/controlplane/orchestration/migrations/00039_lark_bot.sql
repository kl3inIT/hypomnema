-- +goose Up
-- A Lark bot the owner gives work through (issue #136): one bot, the people who may talk to it, the Lark chats
-- it answers in, and the messages it has taken.

-- The bot: a Lark app, whose secret is in the vault under the name kept here, who may talk to it (owner,
-- allowlist, everyone of the app's organization), and the owner's own id at Lark. There is one bot, so one row.
CREATE TABLE lark_bot (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    app_id        TEXT    NOT NULL,
    secret        TEXT    NOT NULL,
    mode          TEXT    NOT NULL,
    owner_open_id TEXT    NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL
) STRICT;

-- The people the owner added, by their id at Lark; the email and name are what he sees.
CREATE TABLE lark_bot_people (
    open_id    TEXT PRIMARY KEY,
    email      TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- A person's direct chat with the bot is one chat with the coordinator. sent_seq is the last message of that chat
-- the bot sent to Lark, so a restart sends nothing twice.
CREATE TABLE lark_bot_chats (
    lark_chat_id TEXT PRIMARY KEY,
    open_id      TEXT    NOT NULL,
    chat_id      TEXT    NOT NULL UNIQUE,
    sent_seq     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
) STRICT;

-- The Lark messages the bot has taken: Lark delivers an event again when it was not acknowledged in time.
CREATE TABLE lark_bot_messages (
    message_id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE lark_bot_messages;

DROP TABLE lark_bot_chats;

DROP TABLE lark_bot_people;

DROP TABLE lark_bot;
