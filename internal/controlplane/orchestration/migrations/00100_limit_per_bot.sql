-- +goose Up
-- How many of a Bot's workers run at once (issue #660): the unsettled Dispatches of the Runs it made, across every
-- node, after OpenMausBot's threads.maxConcurrentPerBot (server/bot-thread-limits.ts). NULL is the default,
-- BotDispatchesDefault; the owner's Runs without a Bot have the default too.
ALTER TABLE bots ADD COLUMN max_dispatches INTEGER CHECK (max_dispatches > 0);
-- Why a ready Task has no Dispatch yet, a reason code the scheduler's last pass found (bot_at_limit:<bot>,
-- nodes_full, no_node, ...), after OpenMausBot's queued reason (server/host/queued-reason.ts); NULL once a Dispatch
-- is created. Writing it does not change the Task's updated_at.
ALTER TABLE tasks ADD COLUMN waiting_reason TEXT;

-- +goose Down
ALTER TABLE tasks DROP COLUMN waiting_reason;
ALTER TABLE bots DROP COLUMN max_dispatches;
