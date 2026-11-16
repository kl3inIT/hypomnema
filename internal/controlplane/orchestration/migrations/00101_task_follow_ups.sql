-- +goose Up
-- More work asked of a Task that succeeded, sent to the worker that did it (issue #666): the Task goes back to
-- ready and its next Dispatch continues the same Conversation, with the note in the worker's instructions, as a
-- resolved Gate's answer is given. Orca sends to a finished worker "as a follow-up Task"
-- (skill-guides/orchestration.md:57-61); Polly sends fixes "to the SAME implementer conversation"
-- (omnigent examples/polly/skills/cross-review/SKILL.md:50-56). Every note is kept, in order.
CREATE TABLE task_follow_ups (
    id         TEXT    PRIMARY KEY,
    task_id    TEXT    NOT NULL REFERENCES tasks (id),
    note       TEXT    NOT NULL CHECK (length(note) > 0),
    -- owner or coordinator, as a question's answered_by.
    asked_by   TEXT    NOT NULL CHECK (asked_by IN ('owner', 'coordinator')),
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX task_follow_ups_task_created ON task_follow_ups (task_id, created_at);

-- The follow-up a Dispatch gives its worker: every Dispatch of the Task gets it until one of them succeeds, so a
-- retry in a new Conversation is not left with the first spec only.
ALTER TABLE dispatches ADD COLUMN follow_up_id TEXT REFERENCES task_follow_ups (id);

-- +goose Down
ALTER TABLE dispatches DROP COLUMN follow_up_id;
DROP TABLE task_follow_ups;
