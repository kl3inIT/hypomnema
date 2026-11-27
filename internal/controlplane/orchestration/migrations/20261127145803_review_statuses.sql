-- +goose Up
-- The `review` commit status the control plane posts on a pull request's head (issue #1279): success when a
-- reviewer's verdict on that head is correct, or when its Task needs no worker review (a quick Task); failure when
-- the verdict is incorrect or blocked. A row waits until it is posted, or dropped because the pull request is no
-- longer open there, its head moved, or the control plane holds no token for its repository. As
-- claude-code-action's agent-approval-check, a status is of one commit: a new head has none until it is reviewed.
CREATE TABLE review_statuses (
    -- The canonical URL of the pull or merge request.
    pull_request TEXT    NOT NULL COLLATE NOCASE CHECK (length(pull_request) > 0),
    -- The full commit SHA, lowercase hex: the verdict's head, or the head read from the host.
    head_sha     TEXT    NOT NULL CHECK (length(head_sha) = 40),
    -- The review Task whose verdict it is, or the quick Task whose pull request it is.
    task_id      TEXT    NOT NULL REFERENCES tasks (id),
    reason       TEXT    NOT NULL CHECK (reason IN ('correct', 'incorrect', 'blocked', 'quick')),
    queued_at    INTEGER NOT NULL,
    done_at      INTEGER,
    outcome      TEXT CHECK (outcome IN ('posted', 'closed', 'moved', 'unfollowed', 'expired', 'superseded')),
    PRIMARY KEY (pull_request, head_sha)
) STRICT;

CREATE INDEX review_statuses_waiting ON review_statuses (queued_at) WHERE done_at IS NULL;

-- The quick Tasks whose pull request needs no worker review, kept when the Task succeeds whatever becomes of its
-- Run (review round 1): each pass finds the pull request by the Task's branch and queues success on its head,
-- while the latest Task pushing to that branch is quick, until it is no longer open, the repository has no token,
-- or a day passed. A branch without a pull request yet is looked up every pass for its first minutes, then less
-- often (looked_at).
CREATE TABLE no_review_requests (
    task_id      TEXT    NOT NULL PRIMARY KEY REFERENCES tasks (id),
    repo_host    TEXT    NOT NULL CHECK (length(repo_host) > 0),
    repo         TEXT    NOT NULL CHECK (length(repo) > 0),
    branch       TEXT    NOT NULL CHECK (length(branch) > 0),
    -- The pull request found, or the one the Task's evidence names; '' until one is.
    pull_request TEXT    NOT NULL DEFAULT '',
    since        INTEGER NOT NULL,
    looked_at    INTEGER NOT NULL DEFAULT 0,
    done_at      INTEGER
) STRICT;

-- The quick Tasks that succeeded before (review round 2), with the pull request the follower found for them: the
-- first pass reads each, and lets go of one no longer open.
INSERT INTO no_review_requests (task_id, repo_host, repo, branch, pull_request, since)
SELECT t.id,
       r.repo_host,
       r.repo,
       COALESCE(t.push_branch, 'hyp/' || t.id),
       COALESCE((SELECT l.url
                 FROM pull_request_lookups l
                          JOIN dispatches d ON d.id = l.dispatch_id
                 WHERE d.task_id = t.id
                   AND l.url <> ''
                 ORDER BY l.since DESC
                 LIMIT 1), ''),
       CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM tasks t
         JOIN runs r ON r.id = t.run_id
WHERE t.kind = 'quick'
  AND t.status = 'succeeded'
  AND t.profile <> 'reviewer'
  AND t.profile NOT LIKE 'reviewer-%'
  AND COALESCE(r.repo, '') <> ''
  AND COALESCE(r.repo_host, '') <> '';

-- Today's pull requests get the status of their latest verdict: the control plane posts it once it starts, on a
-- pull request still open at that head. A verdict that names an abbreviated head names no commit exactly: none.
INSERT INTO review_statuses (pull_request, head_sha, task_id, reason, queued_at)
SELECT v.pull_request, v.head_sha, v.task_id, v.verdict, CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM review_verdicts v
WHERE length(v.head_sha) = 40
  AND NOT EXISTS (SELECT 1
                  FROM review_verdicts n
                  WHERE n.pull_request = v.pull_request COLLATE NOCASE
                    AND (n.created_at > v.created_at OR (n.created_at = v.created_at AND n.task_id > v.task_id)))
ON CONFLICT (pull_request, head_sha) DO NOTHING;

-- +goose Down
DROP TABLE no_review_requests;
DROP TABLE review_statuses;
