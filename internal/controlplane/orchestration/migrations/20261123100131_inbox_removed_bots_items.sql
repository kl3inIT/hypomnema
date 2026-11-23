-- A removed Bot's items pass to the main Bot (issue #1124): a Bot edits only the items it added or was given
-- (inboxstore's edit and notAnotherBots), so the items the Thư ký Bot left when it was removed on 2026-11-23 (#1107)
-- could be closed or updated by no Bot. Removing a Bot now passes them on (inboxstore.PassTx); this passes the ones
-- already left: every item not closed whose Bot, who added it or who it is given to, is no longer there (an item
-- that names no Bot it was added by has none to pass from). Each gets the event PassTx records, by the system, and
-- its revision moves on. Closed items stay as they were.

-- +goose Up
CREATE TEMP TABLE passed AS
SELECT id, revision,
       created_by_kind = 'bot' AND created_by <> '' AND created_by NOT IN (SELECT id FROM bots) AS by_removed,
       assignee_kind = 'bot' AND assignee NOT IN (SELECT id FROM bots) AS to_removed,
       created_by, assignee
FROM todos
WHERE state NOT IN ('done', 'dropped', 'duplicate')
  AND ((created_by_kind = 'bot' AND created_by <> '' AND created_by NOT IN (SELECT id FROM bots))
    OR (assignee_kind = 'bot' AND assignee NOT IN (SELECT id FROM bots)));

INSERT INTO todo_events (todo_id, at, revision, actor_kind, actor, kind, before, after)
SELECT id, CAST(unixepoch('subsec') * 1000 AS INTEGER), revision + 1, 'system', 'bots', 'edited',
       json_patch(
           CASE WHEN by_removed THEN json_object('created_by', created_by) ELSE '{}' END,
           CASE WHEN to_removed THEN json_object('assignee', assignee) ELSE '{}' END),
       json_patch(json_patch(
           json_object('reason', 'Bot ' || CASE WHEN by_removed THEN created_by ELSE assignee END ||
                                 ' was removed; its items pass to the main Bot'),
           CASE WHEN by_removed THEN json_object('created_by', 'bot_mainaaaaaaaaaaaaaaaaaaaaaa') ELSE '{}' END),
           CASE WHEN to_removed THEN json_object('assignee', 'bot_mainaaaaaaaaaaaaaaaaaaaaaa') ELSE '{}' END)
FROM passed;

UPDATE todos
SET revision = revision + 1,
    created_by = CASE WHEN (SELECT by_removed FROM passed WHERE passed.id = todos.id)
                      THEN 'bot_mainaaaaaaaaaaaaaaaaaaaaaa' ELSE created_by END,
    assignee = CASE WHEN (SELECT to_removed FROM passed WHERE passed.id = todos.id)
                    THEN 'bot_mainaaaaaaaaaaaaaaaaaaaaaa' ELSE assignee END,
    updated_at = CAST(unixepoch('subsec') * 1000 AS INTEGER)
WHERE id IN (SELECT id FROM passed);

DROP TABLE passed;

-- +goose Down
-- The removed Bot is gone: its items stay the main Bot's, with the events that say so.
SELECT 1;
