-- name: ListQueuedDispatchesOfNode :many
SELECT * FROM dispatches WHERE node_id = @node_id AND status = 'queued' ORDER BY created_at, id;

-- name: ListStoppingDispatchesOfNode :many
SELECT id, epoch FROM dispatches WHERE node_id = @node_id AND status = 'stopping' ORDER BY created_at, id;

-- name: ListUndeliveredAnswersOfNode :many
SELECT q.* FROM questions q JOIN dispatches d ON d.id = q.dispatch_id
WHERE d.node_id = @node_id AND q.mode = 'wait' AND q.status = 'answered' AND q.delivered_at IS NULL
ORDER BY q.answered_at, q.id;

-- name: ListLiveDispatchesOfNode :many
-- The Dispatches whose worker may run on the node: settled at its hello when it no longer holds them (issue #590).
SELECT * FROM dispatches
WHERE node_id = @node_id AND status IN ('starting', 'running', 'unknown', 'stop_unknown')
ORDER BY created_at, id;
