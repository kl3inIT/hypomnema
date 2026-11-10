-- name: InsertDispatchItem :execrows
INSERT INTO dispatch_items (dispatch_id, seq, kind, data, created_at)
VALUES (@dispatch_id, @seq, @kind, @data, @now)
ON CONFLICT (dispatch_id, seq) DO NOTHING;

-- name: LastDispatchItemSeq :one
SELECT CAST(coalesce(max(seq), 0) AS INTEGER) AS seq FROM dispatch_items WHERE dispatch_id = @dispatch_id;

-- name: ListDispatchItemsAfter :many
SELECT * FROM dispatch_items WHERE dispatch_id = @dispatch_id AND seq > @after ORDER BY seq LIMIT @lim;

-- name: ListDispatchItemsBefore :many
SELECT * FROM dispatch_items WHERE dispatch_id = @dispatch_id AND seq < @before ORDER BY seq DESC LIMIT @lim;

-- name: SetDispatchActivity :exec
UPDATE dispatches SET activity = @activity WHERE id = @id;

-- name: DeleteDispatchItemsSettledBefore :execrows
DELETE FROM dispatch_items
WHERE dispatch_id IN (SELECT d.id FROM dispatches d WHERE d.settled_at < @settled_before);
