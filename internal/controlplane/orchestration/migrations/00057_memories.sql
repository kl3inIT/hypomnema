-- Memories of agents and the one about the owner (issue #21): entries one per line, as a Bot's (bots.memory). An
-- agent's is shared by all its workers and is not part of its Profile's versions; the owner's is given to every
-- worker and Bot. After OpenMausBot's MEMORY.md per bot and About me, and Hermes's MEMORY.md and USER.md
-- (docs/research/2026-11-13-agent-memory.md).

-- +goose Up
CREATE TABLE memories (
    -- agent: an agent's, named by its Profile's name; owner: the one about the owner, named ''.
    kind       TEXT    NOT NULL CHECK (kind IN ('agent', 'owner')),
    name       TEXT    NOT NULL,
    memory     TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (kind, name)
) STRICT;

-- +goose Down
DROP TABLE memories;
