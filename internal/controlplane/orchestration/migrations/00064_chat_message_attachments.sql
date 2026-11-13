-- The files the owner attached to a message (issue #343), as t3code's user message keeps its attachments
-- (packages/contracts/src/chatAttachment.ts) and OpenMausBot's its files by path: a JSON array of name, path in
-- the Bot's folder, size and type, on owner messages only. The files are in the Bot's folder, not here.

-- +goose Up
ALTER TABLE chat_messages ADD COLUMN attachments TEXT CHECK (attachments IS NULL OR json_valid(attachments));

-- +goose Down
ALTER TABLE chat_messages DROP COLUMN attachments;
