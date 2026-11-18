-- name: GetRoleRouting :one
SELECT source FROM role_routing WHERE id = 1;

-- name: SetRoleRouting :exec
INSERT INTO role_routing (id, source) VALUES (1, @source)
ON CONFLICT (id) DO UPDATE SET source = excluded.source;

-- name: RoleMetrics :many
SELECT t.profile AS role, t.kind, c.runtime, CAST(coalesce(d.model, '') AS TEXT) AS model, d.chain_entry,
       count(*) AS attempts, CAST(sum(d.status = 'succeeded') AS INTEGER) AS succeeded,
       CAST(sum(d.status = 'failed') AS INTEGER) AS failed,
       CAST(coalesce(sum(u.cost_micros), 0) AS INTEGER) AS cost_micros,
       CAST(coalesce(sum(u.unpriced), 0) AS INTEGER) AS unpriced
FROM dispatches d JOIN tasks t ON t.id = d.task_id JOIN conversations c ON c.id = d.conversation_id
LEFT JOIN dispatch_usage u ON u.dispatch_id = d.id
WHERE d.settled_at >= @since
GROUP BY t.profile, t.kind, c.runtime, d.model, d.chain_entry
ORDER BY t.profile, t.kind, d.chain_entry, c.runtime, d.model;
