# Tool presets and effective access

Issue #875; approved by the owner on 2026-11-18. The observations about Bot usage are in the issue; no live Bot,
profile or node settings were changed for this work. The starting code on `main` at `e1e8d49a` used allowlists in
`internal/shared/agenttool/switches.go:241`, gated Connection tools in
`internal/controlplane/agentapi/tools.go:143`, and read only the profile's policy for a Dispatch in
`internal/controlplane/orchestration/queries/profiles.sql:101`. `internal/controlplane/orchestration/agents.go:49`
already narrowed Run Connections.

| Decision | Comparison from source |
|---|---|
| Named sets plus additions and denials | OpenClaw (`7eb33cc`) resolves named policies in `src/agents/core-tool-facts.ts:243` and separates a profile, `alsoAllow` and `deny` in `src/config/zod-schema.agent-runtime.ts:500`. Hermes (`05348a3`) uses platform toolset lists in `toolsets.py:196`. The names and exact membership of `full` and `report` are the owner's decision, based on the work described in issue #875. |
| Phone requires an opt-in | Hermes declares `_DEFAULT_OFF_TOOLSETS` in `hermes_cli/tools_config.py:111` and subtracts them from its implicit platform defaults at `hermes_cli/tools_config.py:473`. A phone grant per Bot or profile and coupling it to the Android guide are the owner's decisions; the Hermes mechanism is for general toolsets, not phones. |
| Show saved access, reasons and origin | OpenClaw renders distinct tool access states alongside reasons and their sources in `ui/src/pages/agents/tool-access-diagnostics.tsx:72`. `ui/src/pages/skills/skill-status.tsx:87` shows missing requirements. The profile screen has no particular Run, so a compatible Superset tool is on subject to that Run supplying the Connection. |
| Do not widen a Bot's policy for its workers | Hermes derives the child toolsets from the parent's toolsets in `tools/delegate_tool_toolsets.py:76`; Codex (`bd3eaf7`) takes the child's permission profile from the parent's snapshot in `codex-rs/core/src/agent/child_config.rs:196`. The owner approved using the intersection with the Dispatch's profile, only on Bot Runs, and leaving Connections unchanged. |
