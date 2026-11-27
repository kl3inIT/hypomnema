-- +goose Up
-- The `review` commit status the control plane posts on a pull request's head (issue #1279): success when a
-- reviewer's verdict on that head is correct, or when its Task needs no worker review (a quick Task); failure when
-- the verdict is incorrect or blocked. A row waits until it is posted, or dropped because the pull request is no
-- longer open there, its head moved, or the control plane holds no token for its repository. As
-- claude-code-action's agent-approval-check, a status is of one commit: a new head has none until it is reviewed.
CREATE TABLE review_statuses (
    -- The canonical URL of the pull or merge request.
    pull_request TEXT    NOT NULL COLLATE NOCASE CHECK (length(pull_request) > 0),
    -- A full or abbreviated commit SHA, lowercase hex: the verdict's head, or the head read from the host.
    head_sha     TEXT    NOT NULL CHECK (length(head_sha) BETWEEN 7 AND 40),
    -- The review Task whose verdict it is, or the quick Task whose pull request it is.
    task_id      TEXT    NOT NULL REFERENCES tasks (id),
    reason       TEXT    NOT NULL CHECK (reason IN ('correct', 'incorrect', 'blocked', 'quick')),
    queued_at    INTEGER NOT NULL,
    done_at      INTEGER,
    outcome      TEXT CHECK (outcome IN ('posted', 'closed', 'moved', 'unfollowed', 'expired')),
    PRIMARY KEY (pull_request, head_sha)
) STRICT;

CREATE INDEX review_statuses_waiting ON review_statuses (queued_at) WHERE done_at IS NULL;

-- Today's pull requests get the status of their latest verdict: the control plane posts it once it starts, on a
-- pull request still open at that head.
INSERT INTO review_statuses (pull_request, head_sha, task_id, reason, queued_at)
SELECT v.pull_request, v.head_sha, v.task_id, v.verdict, CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM review_verdicts v
WHERE NOT EXISTS (SELECT 1
                  FROM review_verdicts n
                  WHERE n.pull_request = v.pull_request COLLATE NOCASE
                    AND (n.created_at > v.created_at OR (n.created_at = v.created_at AND n.task_id > v.task_id)))
ON CONFLICT (pull_request, head_sha) DO NOTHING;

-- +goose Down
DROP TABLE review_statuses;
