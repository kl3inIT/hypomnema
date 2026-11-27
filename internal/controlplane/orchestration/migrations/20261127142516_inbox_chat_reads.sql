-- Which of the owner's chats the secretary reads (issue #1270): the owner's choice for one chat of one Connection
-- (integration:label), "the secretary reads it" on or off, and when he set it, as inbox-zero keeps a per-account
-- include or exclude item in its database (apps/web/prisma/schema.prisma, GroupItem.exclude). A chat without a row
-- takes the default of its kind (inboxstore.DefaultReads); a row is always the owner's. A chat switched off is still
-- kept and read on request: only the triage's check skips it. No CHECK on the integration, so that Telegram (#1184)
-- takes these rows when it lands.

-- +goose Up
CREATE TABLE inbox_chat_reads (
    integration TEXT    NOT NULL CHECK (length(integration) BETWEEN 1 AND 40),
    label       TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    chat_id     TEXT    NOT NULL CHECK (length(chat_id) BETWEEN 1 AND 200),
    reads       INTEGER NOT NULL CHECK (reads IN (0, 1)),
    set_at      INTEGER NOT NULL,
    PRIMARY KEY (integration, label, chat_id)
) STRICT;

-- +goose Down
DROP TABLE inbox_chat_reads;
