-- +goose Up
-- The head commit each review Task's verdict covers (issue #953). pstack: "A verdict covers a patch"
-- (skills/poteto-mode/playbooks/shipping.md:7), and a result that does not record its SHAs is dropped
-- (skills/swarm/SKILL.md:37), so only a verdict that names its head is kept. A head keeps one verdict: a new review
-- Task for a head with a correct or incorrect verdict is refused; a blocked one may be run again.
CREATE TABLE review_verdicts (
    task_id      TEXT    NOT NULL REFERENCES tasks (id),
    -- The canonical URL of the pull or merge request.
    pull_request TEXT    NOT NULL CHECK (length(pull_request) > 0),
    -- A full or abbreviated commit SHA, lowercase hex, as the reviewer named it.
    head_sha     TEXT    NOT NULL CHECK (length(head_sha) BETWEEN 7 AND 40),
    verdict      TEXT    NOT NULL CHECK (verdict IN ('correct', 'incorrect', 'blocked')),
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (task_id, head_sha)
) STRICT;

CREATE INDEX review_verdicts_pull_request ON review_verdicts (pull_request COLLATE NOCASE, created_at);

-- +goose Down
DROP TABLE review_verdicts;
