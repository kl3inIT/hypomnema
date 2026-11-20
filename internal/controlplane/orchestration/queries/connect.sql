-- name: ListQueuedDispatchesOfNode :many
SELECT d.* FROM dispatches d JOIN tasks t ON t.id = d.task_id JOIN nodes n ON n.id = d.node_id
WHERE d.node_id = @node_id AND d.status = 'queued'
  AND (n.task_branches <> 0 OR (t.push_branch IS NULL AND t.start_from IS NULL AND t.pr_base IS NULL))
ORDER BY d.created_at, d.id;

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
