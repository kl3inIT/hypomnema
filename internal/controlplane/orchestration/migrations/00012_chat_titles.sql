-- Chats started without a title (from the CLI before it sent one) take their first owner message's first line,
-- at most 80 characters, as new chats do (issue #41).

-- +goose Up
UPDATE chats
SET title = (
    SELECT trim(substr(substr(m.text, 1, instr(m.text || char(10), char(10)) - 1), 1, 80))
    FROM chat_messages m
    WHERE m.chat_id = chats.id AND m.role = 'owner'
    ORDER BY m.seq
    LIMIT 1
)
WHERE title = 'Chat'
  AND EXISTS (SELECT 1 FROM chat_messages m WHERE m.chat_id = chats.id AND m.role = 'owner');

-- +goose Down
-- The default titles are not restored: the titles taken are what the chats are about.
SELECT 1;
