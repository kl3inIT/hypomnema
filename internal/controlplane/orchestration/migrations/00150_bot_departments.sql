-- A Bot may be in one department, a named section of the sidebar the owner makes, names and orders (issue #471), as
-- OpenMausBot's departments: bot.section and the ordered list of departments (server/section-context.ts
-- readSections, server/routes/sidebar-sections.ts). A department shares nothing: it only lists its Bots together.
-- Departments replace the Pinned section of issue #470: the Bots pinned until now go into a department named Pinned,
-- so none moves in the sidebar. A department is kept by id here, not by its name as OpenMausBot keeps it, so a rename
-- changes one row.

-- +goose Up
CREATE TABLE departments (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    position   INTEGER NOT NULL CHECK (position >= 0),
    created_at INTEGER NOT NULL
);

ALTER TABLE bots ADD COLUMN department TEXT REFERENCES departments (id) ON DELETE SET NULL;

INSERT INTO departments (id, name, position, created_at)
SELECT 'dep_pinnedaaaaaaaaaaaaaaaaaaaa', 'Pinned', 0, CAST(unixepoch('subsec') * 1000 AS INTEGER)
WHERE EXISTS (SELECT 1 FROM bots WHERE pinned = 1);

UPDATE bots SET department = 'dep_pinnedaaaaaaaaaaaaaaaaaaaa' WHERE pinned = 1;

ALTER TABLE bots DROP COLUMN pinned;

-- +goose Down
ALTER TABLE bots ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1));

UPDATE bots SET pinned = 1 WHERE department IS NOT NULL;

ALTER TABLE bots DROP COLUMN department;

DROP TABLE departments;
