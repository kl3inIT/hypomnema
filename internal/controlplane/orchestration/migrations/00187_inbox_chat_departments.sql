-- The department a chat of the owner's accounts is in (issue #1206, the owner's rule of 2026-11-27): when the owner
-- puts an item read in a chat into a department, or takes it out of any, the chat is in that department from then on,
-- and an item a Bot adds from it without naming one takes it, as Linear's Asks put every request of a Slack channel
-- in the team the channel is mapped to. A chat is named as an item's source names it: its kind (lark, zalo,
-- whatsapp, gmail), the owner's Connection's label (empty for a Lark source without one) and its id at the service.
-- todo_id is the item whose change taught it.

-- +goose Up
CREATE TABLE todo_chat_departments (
    source_kind    TEXT    NOT NULL CHECK (source_kind IN ('lark', 'zalo', 'whatsapp', 'gmail')),
    source_account TEXT    NOT NULL DEFAULT '',
    source_id      TEXT    NOT NULL CHECK (length(source_id) > 0),
    department_id  TEXT    NOT NULL REFERENCES departments (id) ON DELETE CASCADE,
    todo_id        TEXT    NOT NULL,
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (source_kind, source_account, source_id)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE todo_chat_departments;
