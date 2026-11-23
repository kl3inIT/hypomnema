-- The Bot an Inbox item was passed through (issue #1118): when a Bot gives an item to another Bot, the main Bot to the
-- project Bot that fits or a project Bot back to the main Bot, the item keeps who passed it, so that it shows who has
-- it and through whom ("Hypo -> BeyondPilot"), as Linear's issue keeps its assignee beside the agent it is delegated
-- to. NULL when the item's assignee was set by the owner, the system or a Bot giving it to the owner; every change of
-- the assignee sets it again in the same guarded write.

-- +goose Up
ALTER TABLE todos ADD COLUMN passed_by TEXT;

-- +goose Down
ALTER TABLE todos DROP COLUMN passed_by;
