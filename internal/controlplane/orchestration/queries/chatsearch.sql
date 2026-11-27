-- name: ListOwnerChatsOfBot :many
-- A Bot's chats with the owner that its search and its recall read (issue #805): every chat of the Bot but a
-- Routine's and a Lark chat, by the marks GetChatMarks reads and by the same rules; with whether it is the Bot's
-- main chat. An Inbox item's own chat is one of them.
SELECT c.id, c.title,
       CAST(coalesce((SELECT b.main_chat FROM bots b WHERE b.id = c.bot_id), '') = c.id AS INTEGER) AS main
FROM chats c
WHERE c.bot_id = @bot
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
ORDER BY c.id;

-- name: SearchChatMessages :many
-- The messages of the chats named (a JSON array of ids) that the FTS5 query @match finds, the best first by BM25
-- and then the latest (issue #805, after Hermes's session search and OpenMausBot's recallMessages), each with an
-- excerpt around its words, which @open and @close mark.
SELECT m.seq, m.chat_id, m.role, m.sender_id, m.sender_name, m.created_at,
       CAST(snippet(chat_messages_fts, 0, CAST(@open AS TEXT), CAST(@close AS TEXT), char(8230), 48) AS TEXT) AS excerpt
FROM chat_messages_fts
JOIN chat_messages m ON m.seq = chat_messages_fts.rowid
WHERE chat_messages_fts = CAST(@match AS TEXT)
  AND m.chat_id IN (SELECT value FROM json_each(CAST(@chats AS TEXT)))
ORDER BY bm25(chat_messages_fts), m.seq DESC
LIMIT @lim;

-- name: ChatMessagesBefore :many
-- A window's rows before the message it is around, the nearest first (issue #805, Hermes's scroll).
SELECT * FROM chat_messages WHERE chat_id = @chat_id AND seq < @seq ORDER BY seq DESC LIMIT @lim;

-- name: ChatMessagesFrom :many
-- A window's message and the rows after it, in order.
SELECT * FROM chat_messages WHERE chat_id = @chat_id AND seq >= @seq ORDER BY seq LIMIT @lim;
