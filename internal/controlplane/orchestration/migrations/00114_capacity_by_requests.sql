-- Capacity by requests against allocatable, and room shared between Bots (issue #731, second design; owner,
-- 2026-11-17).
--
-- - nodes.max_dispatches becomes an optional cap, NULL for none, as Nomad's node_max_allocs (0 unset,
--   client/config/config.go:400-402). Every node's value is cleared (the owner's answer to question 1): the
--   requests of its Dispatches against its allocatable decide. The column is added again rather than the table made
--   again: node_tokens, conversations and dispatches reference nodes, and the value is not kept anyway.
-- - nodes.reserved_cpu_millis and reserved_memory_bytes are what the owner keeps back from a node's capacity, 0 by
--   default, as the kubelet's system-reserved and kube-reserved (kubelet/config/v1beta1/types.go:765-782) and
--   Nomad's client reserved (command/agent/config.go:1840).
-- - profile_versions.cpu_request_millis and memory_request_bytes are what a worker of the profile asks for, NULL for
--   the default of building work (DefaultCPURequestMillis, DefaultMemoryRequest), as a Kubernetes container's
--   requests (helper/resource_fit.go:159-178) and a Nomad task's resources (structs.go:2388-2394). The explorer,
--   which reads and does not build, asks for 0.5 core and 512 MiB (owner's answer to question 2).
-- - dispatches.cpu_millis and memory_bytes are the request copied onto a Dispatch when it is created, so editing a
--   profile does not change what a running worker counts, as Nomad keeps AllocatedResources on the allocation
--   (nomad/structs/funcs.go:157-164). Every existing Dispatch gets its profile version's request, or the default;
--   only the unsettled ones are ever summed.
-- - bots.max_dispatches is renamed guaranteed_dispatches and keeps its value: a first claim on room as it frees, no
--   longer a limit, as Kueue's nominalQuota (clusterqueue_types.go:318-329) and YARN's capacity.

-- +goose Up
ALTER TABLE nodes DROP COLUMN max_dispatches;
ALTER TABLE nodes ADD COLUMN max_dispatches INTEGER CHECK (max_dispatches > 0);
ALTER TABLE nodes ADD COLUMN reserved_cpu_millis INTEGER NOT NULL DEFAULT 0 CHECK (reserved_cpu_millis >= 0);
ALTER TABLE nodes ADD COLUMN reserved_memory_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_memory_bytes >= 0);

ALTER TABLE profile_versions ADD COLUMN cpu_request_millis INTEGER CHECK (cpu_request_millis > 0);
ALTER TABLE profile_versions ADD COLUMN memory_request_bytes INTEGER CHECK (memory_request_bytes > 0);
UPDATE profile_versions SET cpu_request_millis = 500, memory_request_bytes = 536870912 WHERE profile = 'explorer';

ALTER TABLE dispatches ADD COLUMN cpu_millis INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dispatches ADD COLUMN memory_bytes INTEGER NOT NULL DEFAULT 0;
UPDATE dispatches
SET cpu_millis   = coalesce((SELECT v.cpu_request_millis FROM profile_versions v WHERE v.id = dispatches.profile_version_id),
                            1000),
    memory_bytes = coalesce((SELECT v.memory_request_bytes FROM profile_versions v
                             WHERE v.id = dispatches.profile_version_id), 3221225472);

ALTER TABLE bots RENAME COLUMN max_dispatches TO guaranteed_dispatches;

-- +goose Down
ALTER TABLE bots RENAME COLUMN guaranteed_dispatches TO max_dispatches;

ALTER TABLE dispatches DROP COLUMN memory_bytes;
ALTER TABLE dispatches DROP COLUMN cpu_millis;

ALTER TABLE profile_versions DROP COLUMN memory_request_bytes;
ALTER TABLE profile_versions DROP COLUMN cpu_request_millis;

ALTER TABLE nodes DROP COLUMN reserved_memory_bytes;
ALTER TABLE nodes DROP COLUMN reserved_cpu_millis;
ALTER TABLE nodes DROP COLUMN max_dispatches;
ALTER TABLE nodes ADD COLUMN max_dispatches INTEGER NOT NULL DEFAULT 2 CHECK (max_dispatches > 0);
