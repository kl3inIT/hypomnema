-- +goose Up
-- The owner's questions as cards at Lark (issue #136): which card shows a question, so that a press of one of
-- its buttons or a reply to it answers that question, and so that the card is closed once the question is.
CREATE TABLE lark_bot_questions (
    question_id TEXT PRIMARY KEY,
    card_id     TEXT    NOT NULL,
    message_id  TEXT    NOT NULL UNIQUE,
    -- seq is the card's last update: Lark takes an update only with a higher one.
    seq         INTEGER NOT NULL DEFAULT 0,
    -- closed_at is when the card was changed to say how the question ended; NULL while it can be answered.
    closed_at   INTEGER,
    created_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE lark_bot_questions;
