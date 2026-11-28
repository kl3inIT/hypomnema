-- name: CountReviewStallTodosOfHead :one
-- An abbreviated head and its full SHA identify the same review (see sameCommit).
SELECT count(*) FROM review_stall_todos
WHERE run_id = @run_id
  AND (substr(head_sha, 1, length(CAST(@head_sha AS TEXT))) = CAST(@head_sha AS TEXT) OR substr(CAST(@head_sha AS TEXT), 1, length(head_sha)) = head_sha);

-- name: InsertReviewStallTodo :exec
INSERT INTO review_stall_todos (run_id, head_sha, pull_request, todo_id, long_report)
VALUES (@run_id, @head_sha, @pull_request, @todo_id, @long_report);

-- name: OpenReviewStallTodosOfRun :many
SELECT t.* FROM review_stall_todos s JOIN todos t ON t.id = s.todo_id
WHERE s.run_id = @run_id AND t.state = 'open' AND t.assignee_kind = 'me';

-- name: OpenReviewStallTodosOfRequest :many
SELECT t.* FROM review_stall_todos s JOIN todos t ON t.id = s.todo_id
WHERE s.pull_request = @pull_request AND t.state = 'open' AND t.assignee_kind = 'me';

-- name: OpenReviewStallRequests :many
SELECT DISTINCT s.pull_request FROM review_stall_todos s
JOIN todos t ON t.id = s.todo_id
WHERE t.state = 'open' AND t.assignee_kind = 'me'
ORDER BY s.pull_request;

-- name: ReviewStallLongReport :one
SELECT long_report FROM review_stall_todos WHERE todo_id = @todo_id;

-- name: ReviewStallCoordinator :one
SELECT c.id AS chat_id, c.bot_id FROM review_stall_todos s
JOIN runs r ON r.id = s.run_id JOIN chats c ON c.id = r.chat_id
WHERE s.todo_id = @todo_id AND r.status = 'open';
