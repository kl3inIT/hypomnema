-- A Bot has an id that never changes, and a name that is only shown and may change (issue #466; owner,
-- 2026-11-15), as a node has (00016) and as OpenMausBot's bots and t3code's projects keep a fixed id beside a name.
-- Every record that named a Bot names its id instead, so a rename touches one row.
--
-- No table is made again: routines and channels have children that go with them on a delete (ON DELETE CASCADE),
-- which dropping them would take. Renaming bots.name to id makes SQLite rewrite the foreign keys of routines and
-- channels to bots (id); the ids are then changed in bots and in every child in the same transaction, the foreign
-- keys being checked at its end (defer_foreign_keys, as 00027 does).
--
-- The Bot that was always there, "coordinator", gets a fixed id, the one the code names it by (DefaultBotID), as
-- Hermes's default profile keeps the id "default" whatever it is shown as. The others get a new one of the shape
-- platform.NewID gives: 26 characters of a-z and 2-7, here from random hex with its digits outside 2-7 mapped to
-- letters.

-- +goose Up
PRAGMA defer_foreign_keys = ON;

CREATE TABLE bot_ids (
    name TEXT NOT NULL PRIMARY KEY,
    id   TEXT NOT NULL UNIQUE
) STRICT;

INSERT INTO bot_ids (name, id)
SELECT name,
       CASE
           WHEN name = 'coordinator' THEN 'bot_mainaaaaaaaaaaaaaaaaaaaaaa'
           ELSE 'bot_' || replace(replace(replace(replace(lower(hex(randomblob(13))), '0', 'q'), '1', 'r'), '8', 's'),
                                  '9', 't')
       END
FROM bots;

ALTER TABLE bots RENAME COLUMN name TO id;
-- The name, shown and changed by the owner: trimmed, 1 to 64 characters, one Bot's only (nodes' rule).
ALTER TABLE bots ADD COLUMN name TEXT NOT NULL DEFAULT '';
UPDATE bots SET name = id;
UPDATE bots SET id = (SELECT i.id FROM bot_ids i WHERE i.name = bots.name);
CREATE UNIQUE INDEX bots_name ON bots (name);

ALTER TABLE routines RENAME COLUMN bot TO bot_id;
UPDATE routines SET bot_id = (SELECT i.id FROM bot_ids i WHERE i.name = routines.bot_id);

ALTER TABLE channels RENAME COLUMN bot TO bot_id;
UPDATE channels SET bot_id = (SELECT i.id FROM bot_ids i WHERE i.name = channels.bot_id);

-- chats.bot keeps its old default ('coordinator') in the table's text, which only a rebuild changes; every insert
-- names the Bot's id.
ALTER TABLE chats RENAME COLUMN bot TO bot_id;
UPDATE chats SET bot_id = coalesce((SELECT i.id FROM bot_ids i WHERE i.name = chats.bot_id), bot_id);

ALTER TABLE runs RENAME COLUMN bot TO bot_id;
UPDATE runs SET bot_id = (SELECT i.id FROM bot_ids i WHERE i.name = runs.bot_id) WHERE bot_id IS NOT NULL;

ALTER TABLE artifacts RENAME COLUMN bot TO bot_id;
UPDATE artifacts SET bot_id = coalesce((SELECT i.id FROM bot_ids i WHERE i.name = artifacts.bot_id), bot_id);

-- A Bot's setup change names it by its id; a profile's keeps the profile's name.
UPDATE setup_changes SET name = coalesce((SELECT i.id FROM bot_ids i WHERE i.name = setup_changes.name), name)
WHERE kind = 'bot';

DROP TABLE bot_ids;

-- +goose Down
PRAGMA defer_foreign_keys = ON;

UPDATE setup_changes SET name = coalesce((SELECT b.name FROM bots b WHERE b.id = setup_changes.name), name)
WHERE kind = 'bot';

UPDATE artifacts SET bot_id = coalesce((SELECT b.name FROM bots b WHERE b.id = artifacts.bot_id), bot_id);
ALTER TABLE artifacts RENAME COLUMN bot_id TO bot;

UPDATE runs SET bot_id = (SELECT b.name FROM bots b WHERE b.id = runs.bot_id) WHERE bot_id IS NOT NULL;
ALTER TABLE runs RENAME COLUMN bot_id TO bot;

UPDATE chats SET bot_id = coalesce((SELECT b.name FROM bots b WHERE b.id = chats.bot_id), bot_id);
ALTER TABLE chats RENAME COLUMN bot_id TO bot;

UPDATE channels SET bot_id = (SELECT b.name FROM bots b WHERE b.id = channels.bot_id);
ALTER TABLE channels RENAME COLUMN bot_id TO bot;

UPDATE routines SET bot_id = (SELECT b.name FROM bots b WHERE b.id = routines.bot_id);
ALTER TABLE routines RENAME COLUMN bot_id TO bot;

-- A name that is no longer a valid key of the old schema stays as it is: the old code reads it as written.
UPDATE bots SET id = name;
DROP INDEX bots_name;
ALTER TABLE bots DROP COLUMN name;
ALTER TABLE bots RENAME COLUMN id TO name;
