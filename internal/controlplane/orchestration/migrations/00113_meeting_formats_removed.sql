-- The notes formats are removed (issue #771): the owner did not want to pick one per meeting, and the notes are
-- sectioned by the meeting's own topics, as Hyprnote's Auto writes a section per main topic when no template is
-- picked (crates/template-app/assets/enhance.format.md.jinja). The owner's formats go with their table. The notes'
-- `format` column stays, as a column is not dropped from a released table, and is written 'auto'.

-- +goose Up
DROP TABLE meeting_formats;

-- +goose Down
CREATE TABLE meeting_formats (
    id         TEXT    NOT NULL PRIMARY KEY,
    name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- JSON [{title, about}].
    sections   TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
