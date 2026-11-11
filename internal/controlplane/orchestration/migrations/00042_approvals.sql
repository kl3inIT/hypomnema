-- +goose Up
-- The service proxy (issue #154): a request to a service that a Policy answered ASK waits for the owner.

-- An Approval is the owner's answer to one such request. It names who asked (a Dispatch, with its Task and Run, or
-- a chat's coordinator), the Connection and the action, and keeps what the owner is shown: the decoded request as
-- JSON, never a token. match_hash is a digest of that decoded request, by which a repeat of the same request is
-- recognised. scope says how far an approval reaches: once (this request, used_at set when it ran) or session
-- (the action for the rest of the Run or the chat).
CREATE TABLE approvals (
    id          TEXT PRIMARY KEY,
    integration TEXT    NOT NULL,
    label       TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    action_name TEXT    NOT NULL,
    dispatch_id TEXT,
    task_id     TEXT,
    run_id      TEXT,
    chat_id     TEXT,
    asked_by    TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    payload     TEXT    NOT NULL,
    match_hash  TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    scope       TEXT    CHECK (scope IN ('once', 'session')),
    used_at     INTEGER,
    created_at  INTEGER NOT NULL,
    decided_at  INTEGER
) STRICT;

CREATE INDEX approvals_status ON approvals (status, created_at);
CREATE INDEX approvals_match ON approvals (match_hash);

-- The owner's answer for one action of one Connection, where it is not the action's default.
CREATE TABLE action_policies (
    integration TEXT    NOT NULL,
    label       TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    policy      TEXT    NOT NULL CHECK (policy IN ('allow', 'ask', 'never')),
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (integration, label, action)
) STRICT;

-- A message of the control plane's own that the coordinator is given as a wake-up without an event of a Run
-- behind it: the owner answered an Approval after its request had ended.
ALTER TABLE chat_messages ADD COLUMN to_coordinator INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE chat_messages DROP COLUMN to_coordinator;

DROP TABLE action_policies;

DROP TABLE approvals;
