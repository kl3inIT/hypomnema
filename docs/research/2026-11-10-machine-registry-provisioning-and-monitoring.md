# Machine registry, provisioning, monitoring and a terminal

Date: 2026-11-10. Question from the owner: a registry of machines (the owner's VPSs and VMs created at a provider),
and on each machine's page its CPU and RAM and a way to run commands. What do the references do, and what can be built
now.

Read in `.tmp/`: Omnara `0ef1711`, Omnigent `cf1021e`, OpenMausBot `1de1164`, Coder (clone on disk), Nezha `3e7d9ba`
with its agent `7deb8de` (WTFPL), Beszel `3392dcc` (MIT). The Nezha clone has more than the classic project (a
time-series store, MCP tools); what is written here is what that code does. "Inferred" marks what no reference states.

## 1. What Hypomnema has today

A `nodes` row (`internal/controlplane/orchestration/migrations/00005_nodes.sql`): name, the SHA-256 of one token, status
`active` or `drained`, `max_dispatches`, the worktree root and CLIs the node reported, `last_seen_at`,
`unavailable_at`. `hypomnema nodes add|list`; the node is installed by hand ([README](../../README.md)). The node dials
out: `POST /node/v1/hello`, then one WebSocket ([ARCHITECTURE](../../ARCHITECTURE.md)). The vault's secret store was
merged on 2026-11-10 (issue #66, pull request #67).

## 2. Registry and enrolment

| | Omnara | Omnigent | OpenMausBot | Coder |
|---|---|---|---|---|
| Record | `machines`: `source_kind` byo or pool, provider, `provider_resource_id`, a lifecycle state, cpu and memory as size (`migrations/000003_execution_resources.sql` L83-160) | One `hosts` table for the user's machines and sandboxes; `sandbox_provider` not null marks a managed one (`omnigent/db/db_models.py` L1329-1435) | No registry in the app: a VPS is an SSH alias in `config.json`, a Boat is a name derived from the bot (`server/boat.ts` L79-90) | `workspace_agents` rows made from what Terraform declared (`coderd/provisionerdserver/provisionerdserver.go` L2998-3030) |
| Lifecycle and connection | Two things: lifecycle (`provisioning`, `provision_failed`, `active`, `deleting`, `delete_failed`, `deleted`) and a view of online, asleep, offline (`000054`) | `online` or `offline`; live is online and seen within 90 s (`stores/host_store.py` L56, L121-139) | Per kind | Two things: agent lifecycle and a computed connection status `connecting`, `connected`, `disconnected`, `timeout` (`coderd/database/modelmethods.go` L768-810) |
| Credential | `omnara_daemon_v1_…` with a checksum, only the SHA-256 kept, no expiry, several per machine, revocable (`000007` L5-21, `internal/bearertoken/token.go`) | The user signs in from the CLI (browser approval); a sandbox gets a launch token, hash kept, 7 days, renewed at half life (`server/managed_hosts.py` L271-317) | Pairing code, single use, 10 min, exchanged for a session (`server/sessions.ts`) | A UUID per agent, stored in the clear, valid while its build is the latest (`coderd/httpmw/workspaceagent.go` L85-104) |
| Install | `GET /install/omnarad.sh`: a manifest per platform with a SHA-256, `omnarad install`, the token from a prompt or `OMNARA_MACHINE_TOKEN`, a systemd user unit (`internal/httpapi/omnarad.sh.tmpl`, `internal/omnarad/service_systemd.go`) | `omnigent host <url>` | Manual guide (`docs/deploy-vps.md`) | A bootstrap script rendered into the VM (`provisionersdk/scripts/bootstrap_linux.sh`) |
| What the machine reports | Daemon version, `{os, arch}` (`internal/machinedaemon/client.go` L370-393) | Version, harnesses and their accounts, capabilities (`omnigent/host/frames.py` L283-330) | The connector's heartbeat: os, kernel, arch, cpus, memory, disk, load (`connector/src/host.ts` L42-119) | Version, plus metadata the template declares |
| Self-update | The daemon polls a release manifest every 5 min (`internal/omnarad/update.go`) | not found | Cloud homes only, by image | The agent is downloaded at each start |
| Placement | The agent's config names a machine or a pool; no labels, no capacity (`internal/agentconfig/compiler.go` L557-610) | The caller picks a host or "managed" (`server/schemas.py` L1571-1605) | One computer per bot; the user picks (`server/computer-selection.ts` L23-39) | A workspace is its own machine |

Common ground: the machine dials out; one long-lived credential per machine with only its hash kept (Omnara,
Omnigent's launch token, OpenMausBot); lifecycle kept apart from connection (Omnara, Coder); no reference places work
by labels.

## 3. Creating machines at a provider

| | Omnara | Omnigent | OpenMausBot | Coder |
|---|---|---|---|---|
| Interface | `Provider`: `PrepareProvisioning`, `ValidateMachineConfig`, `ProvisionMachine`, `InspectMachine`, `CheckConnection`, `DeleteMachine`, optional `WakeMachine` (`internal/machinepool/providers/provider.go` L55-132) | `SandboxLifecycle`: `prepare`, `provision`, `attach`, `keep_alive`, `terminate`, `stop`, `resume`, `is_running` (`omnigent/onboarding/sandboxes/base.py` L490) | None: Boat and docker-over-SSH called directly; Fly is in a closed repository | Terraform through `provisionerd`; transitions `start`, `stop`, `delete` (`provisionersdk/proto/provisioner.proto` L348-351) |
| Implemented | 12 sandbox services (E2B, Daytona, Modal, …); no Fly, no AWS | 14 (E2B, Daytona, Modal, Kubernetes, …); no Fly | boat.dev, a VPS over SSH, local Docker | Whatever Terraform supports |
| Provider key | A secret of the organisation, sealed by `internal/secrets`, or a server environment variable for Omnara's own pools (`executionstore/machine_config.go` L263-292) | Server environment variables (`E2B_API_KEY`) | Plain in `config.json`, mode 0600 | In the provisioner's environment, kept out of the database (`docs/templates/extending-templates/provider-authentication.md` L9-27) |
| How the new machine enrols | The daemon token is minted in the same statement that records the attempt, before the provider is called; the machine gets the API URL, installer URL and token in its environment and runs the installer (`machinepool/manager.go` L111-270, `providers/managed_daemon_launcher.go` L35-74) | The launch token's hash is stored, then `omnigent host` is started inside with the token (`base.py` L1140-1193) | n/a | The token or the cloud's instance identity (`coderd/workspaceresourceauth.go`) |
| A create that half fails | A deterministic name `omnara-mch-…`; a retry looks the machine up before creating; `provision_failed` retried after 10 s, 30 s, 90 s, then deleted (`manager.go` L871-1043) | Terminate and remove the row (`managed_hosts.py` L4385-4445) | The idempotency key is written to a journal before the call and kept 24 h (`server/boat-create-idempotency.ts`) | The Terraform state of a failed build is kept so a later delete can destroy it (`provisionerd/runner/runner.go` L946-948) |
| Delete that fails | Retried after 1 min, 5 min, 30 min, 24 h; abandoned after 6 tries (`queries/execution_resources.sql` L761-784) | Soft delete until the sandbox is gone (L4724-4807) | A delete journal with phases (`server/boat-delete-journal.ts`) | Delete with `orphan` forgets the resources (`coderd/wsbuilder/wsbuilder.go` L138-174) |
| Limits and idle | Per pool: maximum machines, CPU and memory totals, `delete_after_idle_minutes` (`000003` L5-56, `000023`) | A reaper, off by default, after 30 days offline | Sleep after 10 min idle (`server/host/cloud-computer-power.ts`) | Autostop after 7 days by default; quotas are paid features |
| Comparing with the provider's own list | not found | not found | Marks unowned Boats as orphaned (`boat.ts` L665) | Terraform state |

No reference read here has an adapter for a VPS provider (Hetzner, DigitalOcean, Vultr, Fly): Omnara and Omnigent
create sandboxes at sandbox services, OpenMausBot's Fly code is not public, Coder goes through Terraform. An adapter
for the owner's provider is new code behind an interface shaped as Omnara's.

## 4. CPU, RAM and disk

| | Collected with | Sent | Kept | Shown |
|---|---|---|---|---|
| Nezha | gopsutil v4 (`agent/pkg/monitor/monitor.go`) | Pushed every 3 s on a gRPC stream the agent opened; `State` (cpu, memory, swap, disk, network, load, process count) and `Host` (platform, totals, arch, version) (`proto/nezha.proto` L15-48) | Latest in memory; history only when a time-series store is configured, 30 days (`service/singleton/tsdb.go`) | Pushed to the browser every 2 s (`GET /ws/server`) |
| Beszel | gopsutil v4, `/proc` and cgroups (`agent/system.go`) | The hub asks every 60 s, every 1 s while someone watches, over the agent's WebSocket or over SSH (`internal/hub/systems/system_manager.go` L34-35) | Latest in the system's row; 1-minute rows rolled up to 10, 20, 120 and 480 minutes, kept 1 h to 30 days (`internal/records/records.go` L42-68) | Table or grid of cards with the same columns; Recharts (`internal/site`) |
| Coder | The template runs `coder stat cpu` and the like (library `coder/clistat`) | Each value on its own interval, 1 s in the examples (`agent/agent.go` L608-707) | Latest only, in an unlogged table (`workspace_agent_metadata`) | Values on the agent's row, streamed (`site/src/modules/resources/AgentMetadata.tsx`) |
| OpenMausBot | `/proc` and Docker (`connector/src/host.ts`) | In the heartbeat every 30 s | Not in the public repository | No bars on its cards |
| Omnara, Omnigent | not found | | | |

Alerts: Nezha (`model/rule.go`), Beszel (`alerts` collection) and Coder (memory and volume thresholds) have them.

## 5. A terminal and commands on a machine

| | For a person | How | Limits |
|---|---|---|---|
| Nezha | Web terminal, scheduled commands, file manager | The server sends a task with a stream id over the stream the agent already holds; the agent opens a second stream for bytes; first byte 0 is input, 1 is a resize; creack/pty, conpty on Windows (`cmd/dashboard/controller/terminal.go`, `agent/cmd/agent/terminal_session.go`) | Owner or admin; the agent can refuse everything with `disable_command_execute`, off by default; runs as the agent's user; ends when the browser leaves; no audit log found |
| Coder | Web terminal | A WebSocket proxied to the agent's reconnecting PTY; GNU screen or a 64 KiB buffer; lives 5 min without a viewer (`agent/reconnectingpty/`) | Permission `ssh` on the workspace; each connection is reported |
| Omnigent | Terminals of a session | xterm.js to tmux on the runner, through the host's tunnel (`server/routes/terminal_attach.py`) | Only the owner writes; others read |
| Omnara | None for people | The agent's `run_command` tool, a PTY on the daemon (`internal/toolcatalog/catalog.go` L133-190) | Only after execution is granted |
| OpenMausBot | One-off command on a cloud Boat only | `POST /api/bots/{id}/computer/exec` (`server/index.ts` L15289-15404) | Refused on a VPS and on shared computers; 4000 characters |
| Beszel | None | The agent's SSH server turns the PTY off (`agent/server.go` L89-92) | |

Three of six give a person a shell on a registered machine (Nezha, Coder, Omnigent); in Coder and Omnigent it belongs
to the person's own workspace or session. The other three do not have it or refuse it on a machine the user brought.

## 6. Machines on the web

Omnara: a table, with a connection dot, status, last seen, and a row that opens for details
(`frontend/apps/web/src/components/overview/MachinesTable.tsx`). Coder: a table. Beszel: table or grid of cards, the
viewer's choice, both from one set of columns (`internal/site/src/components/systems-table/`). OpenMausBot: cards on a
map, each a live screenshot. Omnigent: no machines page.

## 7. What follows for Hypomnema

The proposal is in the issue that links here. In short: the registry and an installer need nothing else; CPU and RAM
ride the node's heartbeat; a provider needs the vault, which now exists; a terminal is the one part where the
references disagree, so it is designed apart and is off unless the node allows it.
