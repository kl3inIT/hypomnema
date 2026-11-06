# Headless CLI drivers: spike B results (issue #1)

Date: 2026-11-06. Run on zm (Ubuntu 24.04, Linux 6.8) from a scratch Go program (not in the repository) that starts
each CLI as a child process and speaks its protocol on stdin/stdout. Versions: Claude Code 2.1.318, Codex CLI 0.147.0.
Status: Claude Code done; Codex protocol mapped and one turn run, blocked on an expired login; isolation not run yet.

## Claude Code: bidirectional stream-JSON

Command:

```
claude -p --input-format stream-json --output-format stream-json --verbose \
  --permission-mode manual --permission-prompt-tool stdio [--resume <session_id>]
```

This is the protocol the Claude Agent SDK uses underneath, so Go can drive it without Node or the SDK.

- **Start**: send `{"type":"control_request","request_id":…,"request":{"subtype":"initialize"}}`, then the prompt as
  `{"type":"user","message":{"role":"user","content":"…"}}`. The `initialize` response reports the account and
  subscription type the CLI is logged in with, and the available agents, commands and models.
- **Permission prompts reach the host only with `--permission-prompt-tool stdio` and the `initialize` request.** Without
  them, `--permission-prompts host` (the default) still denies automatically with a `system/permission_denied` event.
- **A prompt** arrives as `{"type":"control_request","request":{"subtype":"can_use_tool","tool_name":"Write","input":{…},
  "permission_suggestions":[…],"tool_use_id":…},"request_id":…}`. Answer with
  `{"type":"control_response","response":{"subtype":"success","request_id":…,"response":{"behavior":"allow","updatedInput":{…}}}}`
  or `{"behavior":"deny","message":"…"}`. On deny the model reported the refusal and did not try another way (for
  example writing the file through the shell).
- Read-only commands Claude judges safe (`cat`, `sleep && echo`) ran without a prompt in `manual` mode.
- **Interrupt**: `{"type":"control_request","request_id":…,"request":{"subtype":"interrupt"}}` stops the turn at once;
  the turn ends with `result` subtype `error_during_execution` and the process exits 1.
- **Resume**: a new process with `--resume <session_id>` continued the same conversation. Asked what it had reached, it
  answered that the interrupted command's outcome was unknown, matching the "unknown is a state" rule.
- **Output events**: `system/init` (session id, model, permission mode, tool count), `assistant` (text and `tool_use`
  blocks), `user` (`tool_result` blocks with `is_error`), `rate_limit_event` (five-hour and seven-day windows with
  utilization and reset time), `system/commands_changed`, `system/thinking_tokens`, and one `result` per turn with
  `subtype`, `is_error`, `num_turns`, `total_cost_usd`, `duration_ms`, `usage` (cache and input tokens) and `session_id`.
  Closing stdin after the `result` ends the process.
- Measured: a three-tool turn took about 4 s and reported 0.05–0.11 USD.

## Codex: `codex app-server` JSON-RPC

- Newline-delimited JSON-RPC on stdio, no `jsonrpc` field. `codex app-server generate-json-schema --out <dir>` writes
  the full schema: 95 client methods, 10 server requests, 70 notifications.
- Sequence: `initialize {clientInfo:{name,version}}` → notification `initialized` → `thread/start {cwd, sandbox,
  approvalPolicy}` → `turn/start {threadId, input:[{type:"text",text}]}` → notifications until `turn/completed`
  (`turn.status`: `completed | interrupted | failed | inProgress`). Also `turn/interrupt {threadId, turnId}`,
  `turn/steer`, `thread/resume`, `thread/inject_items`.
- Approvals are server requests (`item/commandExecution/requestApproval`, `item/fileChange/requestApproval`,
  `item/permissions/requestApproval`, `item/tool/requestUserInput`, `mcpServer/elicitation/request`); the reply is
  `{decision: accept | acceptForSession | decline | cancel …}`. `approvalPolicy`: `untrusted | on-request | never` or
  granular; `sandbox`: `read-only | workspace-write | danger-full-access`.
- Usage arrives as `thread/tokenUsage/updated`, limits as `account/rateLimits/updated`.
- **Expired login**: `codex login status` said "Logged in using ChatGPT", but the turn failed after 2 s with an `error`
  notification `{codexErrorInfo:"unauthorized", willRetry:false}` and `turn/completed` status `failed`. A node must
  detect this and report "needs login" instead of retrying.
- Still to run after a fresh login: approve, decline, interrupt and resume.

## Consequences for the design

- The `claude-cli` runtime speaks stream-JSON directly in Go; the `codex` runtime speaks app-server JSON-RPC. Both give
  permission prompts to the host, so the node can turn them into Policy verdicts (ALLOW / DENY / ASK → Gate).
- Login state is part of a node's capabilities and must be checked by a real call, not by `status` commands.
- The account a node is logged in with is visible in Claude's `initialize` response; the node should report it so
  the owner sees which subscription each node spends.

## Isolation (Claude Code)

Reference: durable-harness `packages/harness-sandbox/probes/claude-code/sandbox-in-vm.ts` (commit `c7d2472`). It runs
the pinned `claude` as a separate user with private `HOME`, `TMPDIR` and `CLAUDE_CONFIG_DIR`, and turns on **Claude
Code's own sandbox** through `--settings`: `filesystem.allowWrite` (workspace and private dirs), `denyRead` on the
config directory that holds the credentials, `credentials.envVars` deny, `network.allowedDomains` /
`deniedDomains`, `failIfUnavailable: true`, `allowUnsandboxedCommands: false`. On Linux the sandbox needs
`bubblewrap` and `socat`. Then a list of probe commands checks what a Bash command can see.

On zm (no sudo; the owner is in the `docker` group) the separate user was replaced by a Docker container
(`ubuntu:24.04` + `bubblewrap socat`, run with `--user 1000`, only the workspace and a private `/s` mounted):

- **Sandbox on**: Claude refused to start: "Ubuntu's AppArmor restriction on user namespaces stops bubblewrap here
  … `sandbox.failIfUnavailable` is set — refusing to start without a working sandbox." zm has
  `kernel.apparmor_restrict_unprivileged_userns = 1`. `seccomp=unconfined`, `apparmor=unconfined` and
  `--cap-add SYS_ADMIN,NET_ADMIN` on the container did not help. The fix needs root on the host: install
  bubblewrap's AppArmor profile (Claude Code sandboxing docs) or set the sysctl to 0.
- **Container only, sandbox off**: the Bash tool ran as uid 1000, could not write `/opt`, saw only the container's
  processes, but **read the credentials file** (551 bytes) and listed the config directory, and **reached any host**
  (`example.com` 200).

Consequences:

- A node needs root once at setup to allow bubblewrap (AppArmor profile), and must refuse to run workers when the
  sandbox is unavailable, as `failIfUnavailable` does.
- A container alone is not isolation: the CLI's own login must sit where the worker's commands cannot read it, which
  Claude's sandbox does with `denyRead`. Egress must go through the node proxy on a network with no other route.

## Not done yet

- Codex approve / decline / interrupt / resume (needs `codex login --device-auth` on zm).
- Isolation with the sandbox on, after the AppArmor profile is installed on zm; then the same probe for Codex (its
  `sandbox: workspace-write` uses Landlock and seccomp on Linux).
