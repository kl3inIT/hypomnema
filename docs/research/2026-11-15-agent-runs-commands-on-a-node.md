# An agent runs commands on a node's machine

Date: 2026-11-15. Question from the owner (issue [#543](https://github.com/kl3inIT/hypomnema/issues/543)): the Bot
should run commands on a node's machine as a person does over SSH, sudo included; cleaning a node's disk is one use of
it. How do the references let an agent run a command on another machine, with what approval, and with sudo?

Read in `.tmp/`, in code and the projects' own docs: OpenClaw, Hermes Agent, Coder, Omnara, Nezha (dashboard and
agent), Omnigent, OpenMausBot. Teleport's clone is partial; what is said of it comes from file names and its RFD 0209,
and is marked. "Inferred" marks what no reference states.

## 1. The tool and how the machine is chosen

| Reference | Tool and parameters | Machine | Path |
|---|---|---|---|
| OpenClaw | `exec` with `host=node`, `node`, `command`, `workdir`, `env`, `timeoutSeconds` | Per call or bound per agent; with none, the one connected node that offers `system.run`, else an error | `src/agents/bash-tools.schemas.ts` L25-90, `src/agents/bash-tools.exec-host-node-phases.ts` L121-192 |
| Omnara | `run_command`: `command`, `machine_id`, `shell`, `cwd`, `wait_ms` (≤ 30 s, then a process id), `io_mode` | `machine_id` from `list_machines`; optional when exactly one machine is available | `internal/toolcatalog/catalog.go` L142-200, `harness/tools/machine_tools.go` L592-622 |
| Nezha | MCP `server.exec`: `server_id`, `cmd`, `args`, `cwd`, `env`, `timeout_seconds` (1-300), `stdin`, `max_output_bytes` | `server_id`, checked against the caller's access | `cmd/dashboard/controller/mcp_tools_exec.go` L28-121 |
| Coder | `coder_workspace_execute`: `workspace`, `command`, `timeout`, `workdir`, `run_in_background`, with process output, list and signal tools | `workspace` or `workspace.agent`; a stopped workspace is started | `codersdk/toolsdk/workspace.go` L210-244, `codersdk/toolsdk/workspaceagent.go` L16-116 |
| Hermes | `terminal`: `command`, `background`, `timeout`, `workdir`, `pty`, … | Fixed per profile (`TERMINAL_SSH_HOST`); no per-call machine | `tools/terminal_tool.py` L889-937, L1701-1751 |
| Omnigent | `sys_os_shell`: `command`, `timeout` (120 s) on the host its runner was launched on | Not traced | `tools/builtins/os_env.py` L122-135 |
| OpenMausBot | `vm_exec` only in its own Local VM; connected servers deliberately have no shell | — | `server/drivers/agents-catalog.ts` L363-375; `apps/docs/content/docs/self-hosting/connected-servers.mdx` L31-41 |

## 2. Transport and the user it runs as

- OpenClaw, Omnara, Nezha and Coder send the command over the connection the machine's daemon already holds (the
  node's WebSocket, the daemon's WebSocket, the agent's gRPC stream, the tailnet), not SSH. The daemon runs it with
  `sh -c` (OpenClaw `sh -lc`), as the daemon's own OS user, with no privilege change (OpenClaw
  `src/infra/node-shell.ts` L7-18; Coder `agent/agentproc/process.go` L150; Nezha `cmd/agent/main.go` L797).
- Hermes alone uses SSH: a new `ssh … bash -c` per command over a ControlMaster socket, keys only
  (`tools/environments/ssh.py` L94-101, L174-186).

## 3. Output and time

| Reference | Default / most time | Output |
|---|---|---|
| OpenClaw | 1800 s; synchronous on a node, no background | 200,000 bytes, the head kept (`src/node-host/invoke-run-command.ts` L7, L67-69) |
| Omnara | waits ≤ 30 s, then returns a process id | not read |
| Nezha | 30 s / 300 s | 64 KB per stream by default, 1 MB at most; the process group is killed (`cmd/agent/mcp_exec.go` L19-24, L48-87) |
| Coder | 10 s / 5 min, then background | 32 KB, first and last 16 KB (`workspacetools/execute.go` L18, L25; `agent/agentproc/headtail.go` L14-24) |
| Hermes | 180 s / 600 s, then background | 50,000 bytes head and tail, the rest saved to a file (`tools/tool_output_limits.py` L13) |

## 4. Approval

- No approval by default: OpenClaw (`security=full`, `ask=off`, `docs/tools/exec-approvals.md` L265-276), Omnara
  (`always_allow`, `catalog.go` L484), Coder (only an MCP `DestructiveHint`, `toolsdk.go` L198-203), Nezha (none;
  `disable_command_execute` in the agent's own configuration turns all execution off, `agent/model/config.go` L38).
- Approval by default: Hermes (`manual`, with a blocklist that applies even when approvals are off, for SSH and local
  hosts, `tools/approval.py` L1199-1213, `tools/approval_detection.py` L94-131); Omnigent's `ask_on_os_tools` policy
  (`policies/builtins/safety.py` L258-284; whether it is on by default was not confirmed).

## 5. sudo

- Hermes is the only one with a design for it: the password is the secret `SUDO_PASSWORD` (or the session's cache);
  the backend rewrites a command-position `sudo` to `sudo -S -p ''` and prepends the password to the process's stdin,
  so the model never sees it (`tools/terminal_tool_sudo.py` L456-491). A command that pipes a password to `sudo -S`
  itself is blocked (`tools/approval.py` L1227-1249).
- Coder relies on the machine: its template gives the workspace user `NOPASSWD:ALL` in sudoers
  (`codersdk/toolsdk/toolsdk.go` L1086, L1167-1170).
- OpenClaw's `elevated` leaves the sandbox for the host; it is not root (`docs/tools/exec.md` L126-135). Nezha and
  Omnara have no sudo handling: a command has the daemon user's rights.
- Teleport (partial clone, RFD 0209): MCP Access proxies MCP servers registered as apps, with role-based tool
  allow and deny lists and audit events; no tool that runs a command on an SSH node was found.

## 6. What this means for Hypomnema

- The common shape is one tool, a machine chosen per call, the command sent over the daemon's existing connection,
  `sh -c` as the daemon's user, a timeout and a capped output. Hypomnema's node already holds that connection and
  carries the owner's terminal over it (issue #116, learned from Nezha).
- The owner chose no approval (owner, 2026-11-15: "cho chạy luôn"), as OpenClaw, Omnara, Coder and Nezha default to.
- For sudo, Hermes's way fits a node whose user needs a password, such as zm: the password is a Vault secret, given to
  `sudo -S` by the node, never shown to the agent (owner, 2026-11-15: from the owner's Vault).
