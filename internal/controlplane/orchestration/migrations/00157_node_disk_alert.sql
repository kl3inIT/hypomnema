-- A node's out-of-disk alert (issue #1017), as Coder keeps a volume monitor's state and the time its alert is
-- debounced until (workspace_agent_volume_resource_monitors.state, debounced_until; coderd/agentapi/
-- resources_monitoring.go:213,248-252):
-- - disk_alert: 'ok'; 'low', the node reported it is still below its floor after trimming its caches and the owner
--   was not told, as the alert came within the debounce; 'told', the owner was told and is told again on recovery.
-- - disk_alert_until: until when a new episode is not told (Unix milliseconds).

-- +goose Up
ALTER TABLE nodes ADD COLUMN disk_alert TEXT NOT NULL DEFAULT 'ok' CHECK (disk_alert IN ('ok', 'low', 'told'));
ALTER TABLE nodes ADD COLUMN disk_alert_until INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN disk_alert_until;
ALTER TABLE nodes DROP COLUMN disk_alert;
