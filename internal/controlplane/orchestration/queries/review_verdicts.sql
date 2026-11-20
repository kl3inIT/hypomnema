-- name: RecordReviewVerdict :exec
-- A follow-up on the same review Task that reviews the same head again replaces its verdict.
INSERT INTO review_verdicts (task_id, pull_request, head_sha, verdict, created_at)
VALUES (@task_id, @pull_request, @head_sha, @verdict, @now)
ON CONFLICT (task_id, head_sha) DO UPDATE SET pull_request = excluded.pull_request,
                                              verdict      = excluded.verdict,
                                              created_at   = excluded.created_at;

-- name: ListReviewVerdicts :many
-- Every recorded verdict on a pull request, of any Run, newest first.
SELECT * FROM review_verdicts
WHERE pull_request = @pull_request COLLATE NOCASE
ORDER BY created_at DESC, task_id DESC;
