-- +goose Up
CREATE TABLE review_stall_todos (
    run_id       TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    head_sha     TEXT NOT NULL,
    pull_request TEXT NOT NULL COLLATE NOCASE,
    todo_id      TEXT NOT NULL UNIQUE REFERENCES todos (id) ON DELETE CASCADE,
    PRIMARY KEY (run_id, head_sha)
) STRICT;

CREATE INDEX review_stall_todos_pull_request ON review_stall_todos (pull_request);

-- +goose Down
DROP TABLE review_stall_todos;
