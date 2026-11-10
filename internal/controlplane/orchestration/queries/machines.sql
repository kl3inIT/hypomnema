-- name: InsertMachineProvider :exec
INSERT INTO machine_providers (id, name, kind, key_ref, region, size, max_machines, options, created_at, updated_at)
VALUES (@id, @name, @kind, @key_ref, @region, @size, @max_machines, @options, @now, @now);

-- name: GetMachineProvider :one
SELECT * FROM machine_providers WHERE id = @id;

-- name: GetMachineProviderByName :one
SELECT * FROM machine_providers WHERE name = @name;

-- name: ListMachineProviders :many
SELECT * FROM machine_providers ORDER BY name;

-- name: DeleteMachineProvider :execrows
DELETE FROM machine_providers WHERE id = @id;

-- name: ListMachineProviderNamesByKeyRef :many
SELECT name FROM machine_providers WHERE key_ref = @key_ref ORDER BY name;

-- name: CountLiveNodesOfProvider :one
-- The provider's machines that have not been removed, whatever their state.
SELECT count(*) FROM nodes WHERE provider_id = @provider_id AND status <> 'deleted';

-- name: InsertMachineNode :exec
INSERT INTO nodes (id, name, status, max_dispatches, provider_id, region, size, machine_state, created_at, updated_at)
VALUES (@id, @name, 'active', @max_dispatches, @provider_id, @region, @size, 'provisioning', @now, @now);

-- name: SetMachineResource :execrows
UPDATE nodes SET updated_at = @now, provider_resource_id = @provider_resource_id
WHERE id = @id AND status <> 'deleted' AND provider_id IS NOT NULL;

-- name: FailMachineProvisioning :execrows
-- Only a machine still waited for fails: one that said hello meanwhile is ready.
UPDATE nodes SET updated_at = @now, machine_state = 'provision_failed', machine_error = @error
WHERE id = @id AND machine_state = 'provisioning';

-- name: StartMachineDeletion :execrows
UPDATE nodes
SET updated_at = @now, status = 'drained', machine_state = 'deleting', machine_error = NULL, machine_attempts = 0,
    machine_retry_at = NULL
WHERE id = @id AND status <> 'deleted' AND provider_id IS NOT NULL
  AND machine_state NOT IN ('deleting', 'delete_failed');

-- name: FailMachineDeletion :execrows
UPDATE nodes
SET updated_at = @now, machine_state = 'delete_failed', machine_error = @error,
    machine_attempts = machine_attempts + 1, machine_retry_at = @retry_at
WHERE id = @id AND machine_state IN ('deleting', 'delete_failed');

-- name: FinishMachineDeletion :execrows
UPDATE nodes SET updated_at = @now, status = 'deleted', deleted_at = @now, machine_retry_at = NULL
WHERE id = @id AND machine_state IN ('deleting', 'delete_failed');

-- name: ListUnsettledMachines :many
-- Machines the control plane still owes a step: waited for, being destroyed, or between on and off.
SELECT * FROM nodes
WHERE status <> 'deleted'
  AND (machine_state IN ('provisioning', 'deleting', 'delete_failed') OR machine_power IN ('stopping', 'starting'))
ORDER BY created_at, id;

-- name: ListLiveMachinesOfProvider :many
SELECT * FROM nodes WHERE provider_id = @provider_id AND status <> 'deleted' ORDER BY created_at, id;

-- name: StopMachine :execrows
-- A ready machine that is on takes no more work and is to be stopped.
UPDATE nodes SET updated_at = @now, status = 'drained', machine_power = 'stopping', machine_error = NULL
WHERE id = @id AND status <> 'deleted' AND machine_state = 'ready' AND machine_power IS NULL;

-- name: MachineStopped :execrows
UPDATE nodes SET updated_at = @now, machine_power = 'stopped', machine_error = NULL
WHERE id = @id AND machine_power = 'stopping';

-- name: StartMachine :execrows
-- A stopped machine is to be started and takes work again once its node says hello.
UPDATE nodes SET updated_at = @now, status = 'active', machine_power = 'starting', machine_error = NULL
WHERE id = @id AND status <> 'deleted' AND machine_state = 'ready' AND machine_power = 'stopped';

-- name: SetMachinePowerError :execrows
UPDATE nodes SET updated_at = @now, machine_error = @error
WHERE id = @id AND machine_power IN ('stopping', 'starting');
