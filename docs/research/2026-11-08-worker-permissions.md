# Worker permissions when no one watches (2026-11-08)

What a worker may do without asking, and what bounds it, when an orchestrator runs it unattended. Studied for issue
#32. Revisions read:
- Orca `4c13ae46`;
- Omnigent `cf1021e`;
- OpenClaw `d622a2b`;
- Omnara `0ef1711`;
- durable-harness `c7d2472`;
- Coder `b798061`;
- OpenHands `91c2e38` (SDK `e4160d1`).

| Project | Default for the worker | Prompts | Boundary | Unanswered prompt | Read |
|---|---|---|---|---|---|
| Orca | `full-access`: Claude `--dangerously-skip-permissions`, Codex `--dangerously-bypass-approvals-and-sandbox`. Other modes: `ask`, `accept-edits`, `auto`. An unknown choice falls back to `ask` | None in full access. In `ask` mode, a card in the chat timeline | None | No timeout | `src/shared/default-global-settings.ts` L162, L251; `src/shared/agent-launch/permissions/tui-agent-permissions.ts` L12-45; `tui-agent-permission-choice.ts` L51 |
| OpenHands | `NeverConfirm`. ACP workers run with `bypassPermissions` (Claude) or `agent-full-access` (Codex) | ACP prompts auto-approved with the first option | Docker, if enabled | No timeout | `src/services/settings.ts` L11; SDK `agent/acp_agent.py` L1720-1736, L1866-1873 |
| Coder chatd | No approval step. ACP workers are auto-allowed | An optional `pre_tool_use` hook, which fails closed | The workspace | No prompt | `coderd/x/chatd/prompt.go` L84; `agent/x/agentacp/client.go` L17-29 |
| Omnara | Built-in tools `always_allow`, MCP tools `always_ask` | A permission interaction in the dashboard or API | The machine (BYO or a pooled sandbox) | No timeout | `internal/toolcatalog/permissions.go` L9-19; `internal/harness/tools/approvals.go` L136-160 |
| Omnigent | Claude `permission_mode="auto"`, plus ALLOW/DENY/ASK policies (none by default; built-ins are opt-in) | ASK goes to a web approval card or URL | bubblewrap sandbox on by default; refuses to run without it | Denied after a day | `omnigent/inner/claude_sdk_harness.py` L135; `runtime/policies/engine.py` L55-58; `docs/AGENT_YAML_SPEC.md` L548, L600-626; `spec/types.py` L40 |
| OpenClaw | Shell `security: full`, `ask: off`; ACP workers `approve-reads` | Companion app or chat channels | Sandbox off by default | Denied after 30 min | `docs/tools/exec-approvals.md` L23-34, L259-287; `docs/tools/acp-agents-setup.md` L560 |
| durable-harness | Claude `dontAsk` with only its bridge tools allowed; Codex `untrusted` | Decided by the harness after the intent is logged | A VM | Not applicable | `packages/harness-claude-code/src/harness.ts` L637-652; `harness-codex/src/harness.ts` L909 |

## Common ground

- Most run unattended workers without prompts: Orca, OpenHands, Coder, Omnara for its built-ins, and Omnigent in
  `auto`.
- The boundary is the machine, the workspace or a sandbox, not the prompts. Only Omnigent turns a sandbox on by
  default.
- Where prompts exist, rules decide first. ASK goes to a card or a chat, and an unanswered prompt is denied
  (OpenClaw after 30 min, Omnigent after a day).

## What Hypomnema takes

Decided by the owner on 2026-11-08, for the first runs:
- **The default:** every permission prompt is allowed and logged (`node.tool_allowed`), as Orca, OpenHands and Coder
  do by default.
- **The boundary:** a machine or OS user that holds only the node: its token, the account's Claude token, and a `gh`
  login.
- **Later:** ALLOW/DENY/ASK rules as Omnigent's, with ASK as a question to the owner that is denied after a day.
  They come with the vault and the sandbox.

The permission prompts still reach the node through `--permission-prompt-tool stdio`, so the rules can be added
without changing how workers run.
