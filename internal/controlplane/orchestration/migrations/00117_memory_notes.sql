-- The notes of a memory read only when needed (issue #589, docs/research/2026-11-16-memory-as-a-directory.md and
-- 2026-11-18-long-term-memory.md): the files of an agent's or a Bot's memory directory other than MEMORY.md and
-- USER.md, which stay the memories table and bots.memory. A path is relative to /memories, at most two parts. Kept by
-- the control plane so every worker on every node sees one directory, as Coder keeps its project memories in its
-- database (coderd/database/migrations/000612_chat_project_memories.up.sql); 200 notes of 8 KB at most, its limits.

-- +goose Up
CREATE TABLE memory_notes (
    -- agent: an agent's, named by its Profile's name; bot: a Bot's, named by its id.
    kind       TEXT    NOT NULL CHECK (kind IN ('agent', 'bot')),
    name       TEXT    NOT NULL,
    path       TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (kind, name, path)
) STRICT;

-- +goose Down
DROP TABLE memory_notes;
