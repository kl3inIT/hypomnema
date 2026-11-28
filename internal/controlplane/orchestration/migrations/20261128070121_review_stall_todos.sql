-- +goose Up
CREATE TABLE review_stall_todos (
    run_id       TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    head_sha     TEXT,
    pull_request TEXT NOT NULL COLLATE NOCASE,
    todo_id      TEXT NOT NULL PRIMARY KEY REFERENCES todos (id) ON DELETE CASCADE,
    long_report  TEXT NOT NULL DEFAULT '',
    UNIQUE (run_id, head_sha)
) STRICT;

CREATE INDEX review_stall_todos_pull_request ON review_stall_todos (pull_request);

-- +goose Down
DROP TABLE review_stall_todos;
