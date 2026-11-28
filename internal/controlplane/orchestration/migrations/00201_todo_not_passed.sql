-- +goose Up
-- Why the main Bot passed a to-do given to it to no project Bot (issue #1334), shown as "Not passed" in the Inbox
-- instead of the item staying with it silently, as Paperclip posts a visible "unresolved routing" comment when no
-- route matches (docs/specs/external-task-protocol.md, section 8.1). NULL when it said none; cleared when the item is
-- given to someone else.
ALTER TABLE todos ADD COLUMN not_passed TEXT CHECK (not_passed IS NULL OR length(not_passed) > 0);

-- +goose Down
ALTER TABLE todos DROP COLUMN not_passed;
