-- The open Runs left on a Bot's former main chat move to its main chat, once (issue #1169). Since then a new main
-- chat takes the former one's open Runs (orchestration/run_custody.go moveMainRunsTx); before, they stayed and woke a
-- chat the owner no longer read, whose pi did not finish them (13 Runs on 2026-11-25, finished by hand).
--
-- A former main chat is any chat of a Bot that has a main chat, other than it, that is not a Routine's chat, a Lark
-- chat (both marked as queries/chats.sql GetChatMarks marks them) or an Inbox item's own chat (todos.chat_id, issue
-- #1111): the web and the CLI make every other chat as the Bot's main chat (StartMainChat, OpenMainChat), and the
-- chats from before main chats (migration 00138) were the owner's conversation with the Bot alike. A Run on a
-- Routine's, a Lark or an Inbox item's chat stays there. Each moved Run is woken by its events from now on, as
-- BindRunToChat binds one; the wake-ups the former chat's pi was not given yet are not given to it; the main chat
-- gets a wake-up naming the Runs that came, the former chat a line saying where they went.

-- +goose Up
CREATE TEMP TABLE moved AS
SELECT r.id, r.chat_id AS from_chat, b.main_chat AS to_chat
FROM runs r
JOIN chats c ON c.id = r.chat_id
JOIN bots b ON b.id = c.bot_id
WHERE r.status = 'open'
  AND b.main_chat IS NOT NULL
  AND r.chat_id <> b.main_chat
  AND NOT EXISTS (SELECT 1 FROM routine_runs rr WHERE rr.chat_id = c.id)
  AND NOT EXISTS (
        SELECT 1 FROM chat_messages m
        WHERE m.chat_id = c.id AND m.kind = 'wake' AND m.event_seq IS NULL AND m.to_coordinator = 1
          AND m.text LIKE 'Routine %'
    )
  AND NOT EXISTS (SELECT 1 FROM chat_messages m WHERE m.chat_id = c.id AND m.read_run = 1)
  AND c.channel_group = 0
  AND NOT EXISTS (SELECT 1 FROM channel_chats cc WHERE cc.chat_id = c.id)
  AND NOT EXISTS (
        SELECT 1 FROM chat_messages m WHERE m.chat_id = c.id AND m.role = 'owner' AND m.sender_id IS NOT NULL
    )
  AND NOT (c.title LIKE 'Lark: %' AND EXISTS (
        SELECT 1 FROM chat_messages m
        WHERE m.chat_id = c.id AND m.role = 'owner' AND m.sender_id IS NULL
          AND substr(m.text, 1, length('[Lark message from ' || substr(c.title, 7) || ', who is not the owner.'))
              = '[Lark message from ' || substr(c.title, 7) || ', who is not the owner.'
    ))
  AND NOT EXISTS (SELECT 1 FROM todos t WHERE t.chat_id = c.id);

UPDATE chat_messages SET delivered_at = CAST(unixepoch('subsec') * 1000 AS INTEGER)
WHERE delivered_at IS NULL
  AND event_seq IN (SELECT e.seq FROM events e JOIN moved ON moved.id = e.run_id
                    WHERE moved.from_chat = chat_messages.chat_id);

UPDATE runs
SET chat_id = (SELECT to_chat FROM moved WHERE moved.id = runs.id),
    wake_after = coalesce((SELECT max(seq) FROM events WHERE run_id = runs.id), 0),
    updated_at = CAST(unixepoch('subsec') * 1000 AS INTEGER)
WHERE id IN (SELECT id FROM moved);

INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
SELECT to_chat, 'system', 'wake',
       CASE WHEN count(*) = 1 THEN 'Run ' ELSE 'Runs ' END || group_concat(id, ', ') || ' moved here from chat ' ||
       from_chat || ', the Bot''s main chat before; this chat coordinates them now and their events wake it. ' ||
       'Read each one''s current Tasks, unanswered questions and review evidence before acting: ' ||
       'hypomnema runs show <run> --json',
       1, CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM (SELECT * FROM moved ORDER BY from_chat, id)
GROUP BY from_chat, to_chat;

INSERT INTO chat_messages (chat_id, role, kind, text, to_coordinator, created_at)
SELECT from_chat, 'system', 'wake',
       CASE WHEN count(*) = 1 THEN 'Run ' ELSE 'Runs ' END || group_concat(id, ', ') || ' moved to chat ' || to_chat ||
       ', the Bot''s main chat now; their events wake that chat.',
       0, CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM (SELECT * FROM moved ORDER BY from_chat, id)
GROUP BY from_chat, to_chat;

UPDATE chats SET updated_at = CAST(unixepoch('subsec') * 1000 AS INTEGER)
WHERE id IN (SELECT to_chat FROM moved);

DROP TABLE moved;

-- +goose Down
-- The Runs stay in the main chat, with the lines that say they moved: the former chat is not read.
SELECT 1;
