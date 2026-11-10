-- A worker's transcript on the server (issue #43): what the worker said and did during one Dispatch, as items the
-- node sends in order. An item is whole and normalized, as Omnara's agent_events and Omnigent's
-- conversation_items; the node bounds its size. A Dispatch also keeps one line about its newest item.

-- +goose Up
CREATE TABLE dispatch_items (
    dispatch_id TEXT    NOT NULL REFERENCES dispatches (id),
    -- Numbered by the node from 1; an item sent again keeps its number and is ignored.
    seq         INTEGER NOT NULL CHECK (seq > 0),
    kind        TEXT    NOT NULL CHECK (kind IN ('message', 'reasoning', 'tool_call', 'tool_result', 'notice')),
    -- JSON: text, call_id, tool, input, failed; each kind uses its own.
    data        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (dispatch_id, seq)
) STRICT;

ALTER TABLE dispatches ADD COLUMN activity TEXT;

-- +goose Down
ALTER TABLE dispatches DROP COLUMN activity;
DROP TABLE dispatch_items;
