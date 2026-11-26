-- An Inbox item's project is one of the departments the sidebar lists Bots in (issue #1188, owner 2026-11-26), not
-- a list of its own: #1157's inbox_projects goes, and todos.project_id names a department, with a foreign key that
-- leaves an item without a project when its department is removed, as bots.department does (00150). Linear keeps
-- one project an issue.
--
-- No item's project is carried: on zm, on 2026-11-26, inbox_projects had no row and no item had a project. SQLite
-- cannot add a foreign key to a column in place, so project_id is dropped and added again, last in todos as it
-- was; its index goes first, since a column with an index cannot be dropped.

-- +goose Up
DROP INDEX todos_project;

ALTER TABLE todos DROP COLUMN project_id;
ALTER TABLE todos ADD COLUMN project_id TEXT REFERENCES departments (id) ON DELETE SET NULL;

CREATE INDEX todos_project ON todos (project_id) WHERE project_id IS NOT NULL;

DROP INDEX inbox_projects_name;
DROP TABLE inbox_projects;

-- +goose Down
-- The way back has the list of 00169 again, empty: an item's department is not one of its projects, so the items
-- are left without one.
CREATE TABLE inbox_projects (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    -- #rrggbb, lower case.
    color      TEXT    NOT NULL CHECK (color GLOB '#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX inbox_projects_name ON inbox_projects (name COLLATE NOCASE);

DROP INDEX todos_project;

ALTER TABLE todos DROP COLUMN project_id;
ALTER TABLE todos ADD COLUMN project_id TEXT;

CREATE INDEX todos_project ON todos (project_id) WHERE project_id IS NOT NULL;
