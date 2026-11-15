-- Every Bot has a place in one order the owner sets by dragging in the sidebar, and may be pinned (issue #470), as
-- OpenMausBot keeps one fleet order with a separate pinned flag (server/store.ts reorderBots; shared/bot-order.ts)
-- and onyx a dense display_order (backend/onyx/db/models.py). position is dense, 0 to n-1; the Bots there are
-- numbered by when they were made, as ListBots lists them, so the main Bot, made first, is 0. A new Bot goes last.

-- +goose Up
ALTER TABLE bots ADD COLUMN position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0);
ALTER TABLE bots ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1));

UPDATE bots SET position = (
    SELECT count(*) FROM bots b
    WHERE b.created_at < bots.created_at OR (b.created_at = bots.created_at AND b.name < bots.name)
);

-- +goose Down
ALTER TABLE bots DROP COLUMN pinned;
ALTER TABLE bots DROP COLUMN position;
