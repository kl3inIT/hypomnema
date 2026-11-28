-- name: CountReviewStallTodosOfHead :one
-- An abbreviated head and its full SHA identify the same review (see sameCommit).
SELECT count(*) FROM review_stall_todos
WHERE run_id = @run_id
  AND (head_sha = @head_sha OR (head_sha <> '' AND @head_sha <> '' AND
        (substr(head_sha, 1, length(@head_sha)) = @head_sha OR substr(@head_sha, 1, length(head_sha)) = head_sha)));

-- name: InsertReviewStallTodo :exec
INSERT INTO review_stall_todos (run_id, head_sha, pull_request, todo_id)
VALUES (@run_id, @head_sha, @pull_request, @todo_id);

-- name: OpenReviewStallTodosOfRun :many
SELECT t.* FROM review_stall_todos s JOIN todos t ON t.id = s.todo_id
WHERE s.run_id = @run_id AND t.closed_at IS NULL;

-- name: OpenReviewStallTodosOfRequest :many
SELECT t.* FROM review_stall_todos s JOIN todos t ON t.id = s.todo_id
WHERE s.pull_request = @pull_request AND t.closed_at IS NULL;

-- name: OpenReviewStallRequests :many
SELECT DISTINCT s.pull_request FROM review_stall_todos s
JOIN todos t ON t.id = s.todo_id
WHERE t.closed_at IS NULL
ORDER BY s.pull_request;
