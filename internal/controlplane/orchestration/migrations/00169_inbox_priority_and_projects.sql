-- An Inbox item's priority and project (issue #1157), as Linear's issue has both (docs: Priority, Projects) and
-- circle's mock data keeps them (mock-data/priorities.tsx, mock-data/issues.ts:40, the same 0-4 scale): 0 no
-- priority, 1 urgent, 2 high, 3 medium, 4 low, with a one-line reason, which the main Bot gives when it sets it; and one
-- optional project from a small list the owner keeps (name and colour). A project removed leaves its items without one,
-- each with its event, in the removal's transaction (inboxstore.DeleteProject): todos.project_id names it without a
-- foreign key, as todos.chat_id names its chat, so that the column can be dropped again.

-- +goose Up
CREATE TABLE inbox_projects (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    -- #rrggbb, lower case.
    color      TEXT    NOT NULL CHECK (color GLOB '#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- Two projects never share a name, whatever its case.
CREATE UNIQUE INDEX inbox_projects_name ON inbox_projects (name COLLATE NOCASE);

ALTER TABLE todos ADD COLUMN priority INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 4);
ALTER TABLE todos ADD COLUMN priority_reason TEXT NOT NULL DEFAULT '' CHECK (length(priority_reason) <= 300);
ALTER TABLE todos ADD COLUMN project_id TEXT;

CREATE INDEX todos_project ON todos (project_id) WHERE project_id IS NOT NULL;

-- +goose Down
DROP INDEX todos_project;

ALTER TABLE todos DROP COLUMN project_id;
ALTER TABLE todos DROP COLUMN priority_reason;
ALTER TABLE todos DROP COLUMN priority;

DROP TABLE inbox_projects;
