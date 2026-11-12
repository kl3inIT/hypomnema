-- Lark Channels (issue #169): a Channel is a door to one Bot, a Lark app and who may talk through it. There was one
-- Lark bot, which talked to the first Bot; there are several Channels now, each named, each with the Bot it talks
-- to, after OpenClaw's bindings (one channel account routed to one agent). The bot that exists becomes the Channel
-- "main", which talks to the Bot "coordinator". Every table of the bot gets its Channel, so the tables are made
-- again and their rows copied: SQLite adds no referencing NOT NULL column to a table that has rows.

-- +goose Up
-- One Lark app is one Channel (Hermes refuses two profiles on one app), so app_id is unique. secret names the vault
-- secret with the app's secret.
CREATE TABLE lark_channels (
    name          TEXT    NOT NULL PRIMARY KEY CHECK (length(name) > 0),
    app_id        TEXT    NOT NULL UNIQUE,
    secret        TEXT    NOT NULL,
    bot           TEXT    NOT NULL REFERENCES bots (name),
    mode          TEXT    NOT NULL,
    owner_open_id TEXT    NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

INSERT INTO lark_channels (name, app_id, secret, bot, mode, owner_open_id, enabled, created_at, updated_at)
SELECT 'main', app_id, secret, 'coordinator', mode, owner_open_id, enabled, updated_at, updated_at FROM lark_bot;

-- The people the owner added to a Channel. Lark gives a person another open_id in each app, so the same person on
-- two Channels is two rows.
CREATE TABLE lark_channel_people (
    channel    TEXT    NOT NULL REFERENCES lark_channels (name) ON DELETE CASCADE,
    open_id    TEXT    NOT NULL,
    email      TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (channel, open_id)
) STRICT;

INSERT INTO lark_channel_people (channel, open_id, email, name, created_at)
SELECT 'main', open_id, email, name, created_at FROM lark_bot_people
WHERE EXISTS (SELECT 1 FROM lark_channels WHERE name = 'main');

-- A person's direct chat with a Channel's app is one chat with the Channel's Bot. A Lark chat's id is of one app,
-- so it stays the key. sent_seq is the last message of that chat sent to Lark, so a restart sends nothing twice.
CREATE TABLE lark_channel_chats (
    lark_chat_id TEXT    NOT NULL PRIMARY KEY,
    channel      TEXT    NOT NULL REFERENCES lark_channels (name) ON DELETE CASCADE,
    open_id      TEXT    NOT NULL,
    chat_id      TEXT    NOT NULL UNIQUE,
    sent_seq     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX lark_channel_chats_channel ON lark_channel_chats (channel);

INSERT INTO lark_channel_chats (lark_chat_id, channel, open_id, chat_id, sent_seq, created_at)
SELECT lark_chat_id, 'main', open_id, chat_id, sent_seq, created_at FROM lark_bot_chats
WHERE EXISTS (SELECT 1 FROM lark_channels WHERE name = 'main');

-- The Lark messages a Channel has taken: Lark delivers an event again when it was not acknowledged in time.
CREATE TABLE lark_channel_messages (
    message_id TEXT    NOT NULL PRIMARY KEY,
    channel    TEXT    NOT NULL REFERENCES lark_channels (name) ON DELETE CASCADE,
    created_at INTEGER NOT NULL
) STRICT;

INSERT INTO lark_channel_messages (message_id, channel, created_at)
SELECT message_id, 'main', created_at FROM lark_bot_messages
WHERE EXISTS (SELECT 1 FROM lark_channels WHERE name = 'main');

-- The owner's questions as cards at Lark: which card shows a question, and the Channel it was sent through.
CREATE TABLE lark_channel_questions (
    question_id TEXT    NOT NULL PRIMARY KEY,
    channel     TEXT    NOT NULL REFERENCES lark_channels (name) ON DELETE CASCADE,
    card_id     TEXT    NOT NULL,
    message_id  TEXT    NOT NULL UNIQUE,
    -- seq is the card's last update: Lark takes an update only with a higher one.
    seq         INTEGER NOT NULL DEFAULT 0,
    -- closed_at is when the card was changed to say how the question ended; NULL while it can be answered.
    closed_at   INTEGER,
    created_at  INTEGER NOT NULL
) STRICT;

INSERT INTO lark_channel_questions (question_id, channel, card_id, message_id, seq, closed_at, created_at)
SELECT question_id, 'main', card_id, message_id, seq, closed_at, created_at FROM lark_bot_questions
WHERE EXISTS (SELECT 1 FROM lark_channels WHERE name = 'main');

DROP TABLE lark_bot_questions;

DROP TABLE lark_bot_messages;

DROP TABLE lark_bot_chats;

DROP TABLE lark_bot_people;

DROP TABLE lark_bot;

-- +goose Down
-- The one bot there was is the Channel "main"; the other Channels have no place in the tables before.
CREATE TABLE lark_bot (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    app_id        TEXT    NOT NULL,
    secret        TEXT    NOT NULL,
    mode          TEXT    NOT NULL,
    owner_open_id TEXT    NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL
) STRICT;

INSERT INTO lark_bot (id, app_id, secret, mode, owner_open_id, enabled, updated_at)
SELECT 1, app_id, secret, mode, owner_open_id, enabled, updated_at FROM lark_channels WHERE name = 'main';

CREATE TABLE lark_bot_people (
    open_id    TEXT PRIMARY KEY,
    email      TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

INSERT INTO lark_bot_people (open_id, email, name, created_at)
SELECT open_id, email, name, created_at FROM lark_channel_people WHERE channel = 'main';

CREATE TABLE lark_bot_chats (
    lark_chat_id TEXT PRIMARY KEY,
    open_id      TEXT    NOT NULL,
    chat_id      TEXT    NOT NULL UNIQUE,
    sent_seq     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
) STRICT;

INSERT INTO lark_bot_chats (lark_chat_id, open_id, chat_id, sent_seq, created_at)
SELECT lark_chat_id, open_id, chat_id, sent_seq, created_at FROM lark_channel_chats WHERE channel = 'main';

CREATE TABLE lark_bot_messages (
    message_id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL
) STRICT;

INSERT INTO lark_bot_messages (message_id, created_at)
SELECT message_id, created_at FROM lark_channel_messages WHERE channel = 'main';

CREATE TABLE lark_bot_questions (
    question_id TEXT PRIMARY KEY,
    card_id     TEXT    NOT NULL,
    message_id  TEXT    NOT NULL UNIQUE,
    seq         INTEGER NOT NULL DEFAULT 0,
    closed_at   INTEGER,
    created_at  INTEGER NOT NULL
) STRICT;

INSERT INTO lark_bot_questions (question_id, card_id, message_id, seq, closed_at, created_at)
SELECT question_id, card_id, message_id, seq, closed_at, created_at FROM lark_channel_questions WHERE channel = 'main';

DROP TABLE lark_channel_questions;

DROP TABLE lark_channel_messages;

DROP TABLE lark_channel_chats;

DROP TABLE lark_channel_people;

DROP TABLE lark_channels;
