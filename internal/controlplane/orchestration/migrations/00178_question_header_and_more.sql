-- A question may hold more than one decision (issue #1219, decisions 1 and 3): the first keeps text and options,
-- now with a short header, and the next ones are kept in more, a JSON list of {header, text, options}, answered in
-- the same order in the answer's more. An option may carry a description, inside the options JSON, which needs no
-- column. A question asked before keeps an empty header and no more, so it reads as one decision, as it did.
-- From Codex's request_user_input (1-3 questions, each with a header; request_user_input_spec.rs:39-72) and
-- opencode's question (a header of at most 30 characters; packages/schema/src/question.ts:11-16).
--
-- The CHECK sits on the column itself, so the Down's DROP COLUMN works (SQLite refuses to drop a column another
-- CHECK names).

-- +goose Up
ALTER TABLE questions ADD COLUMN header TEXT NOT NULL DEFAULT '' CHECK (length(header) <= 30);
ALTER TABLE questions ADD COLUMN more TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE questions DROP COLUMN more;
ALTER TABLE questions DROP COLUMN header;
