-- The name of each removed Bot (issue #1139): a Bot's row goes when it is removed, but its id stays in what it did
-- (the Inbox's items and their events, the gateway's usage), so the clients name it "<name> (removed)", as Slack
-- names a deactivated member, and never by its id. A Bot put back by an undo of its removal leaves this table.
--
-- The Thư ký Bot was removed on 2026-11-23 (#1107), before its name was kept; its name is nowhere else in the
-- database, so it is written here, and only where something still names its id.

-- +goose Up
CREATE TABLE removed_bots (
    id   TEXT NOT NULL PRIMARY KEY CHECK (length(id) > 0),
    name TEXT NOT NULL
) STRICT;

INSERT INTO removed_bots (id, name)
SELECT 'bot_6lwjzodlaqoat2ktg47yztjfae', 'Thư ký'
WHERE NOT EXISTS (SELECT 1 FROM bots WHERE id = 'bot_6lwjzodlaqoat2ktg47yztjfae')
  AND (EXISTS (SELECT 1 FROM todos
               WHERE created_by = 'bot_6lwjzodlaqoat2ktg47yztjfae' OR assignee = 'bot_6lwjzodlaqoat2ktg47yztjfae'
                  OR passed_by = 'bot_6lwjzodlaqoat2ktg47yztjfae')
    OR EXISTS (SELECT 1 FROM todo_events WHERE actor = 'bot_6lwjzodlaqoat2ktg47yztjfae'));

-- +goose Down
DROP TABLE removed_bots;
