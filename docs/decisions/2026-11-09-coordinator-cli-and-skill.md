# Coordinators drive orchestration with the CLI and a skill

Date: 2026-11-09. Status: accepted by the owner, implemented by issue #41 (PR 2). Replaces the intent in
[vision.md](../vision.md) that the coordinator's tools come through MCP and a pi extension.

## Context

A coordinator is pi on the server or the owner's own agent (Claude Code, Codex) on a laptop. It needs to create
Runs and Tasks, wait for results, and answer or forward its workers' questions, all through the client API
(`/api/v1`). Issue #41 first proposed an MCP server at `/mcp` and pi restricted to MCP tools only. The owner asked
that pi keep its own tools, as every reference's coordinator does, and pointed out that MCP tool definitions cost
tokens on every model call.

## Decision

- Coordinators use the `hypomnema` CLI, which calls the client API with a coordinator token, and a coordinator
  skill, as Orca's coordinators use `orca orchestration` and its skill.
- Every command takes `--json`; long text comes from a file or standard input; `runs wait` blocks until something
  needs the coordinator and prints the next commands.
- The skill is Orca's shape: an installed stub runs `hypomnema skills get hypomnema-coordinator`, which prints the
  guide compiled into the binary; rare branches are references. `hypomnema skills install` writes the stub to
  `~/.agents/skills` (pi, Codex) and `~/.claude/skills` (Claude Code) and replaces only copies that match a
  released stub.
- pi keeps its own tools (read, shell) and edits no code.

## Alternatives considered

- **MCP at `/mcp`** (Omnara, Coder, OpenClaw): suits clients without a shell. With `direct` exposure every tool is
  declared on every request; `deferred` adds a search step. It stays possible later as another door to the same API.
- **MCP only for pi, with no shell**: no reference restricts its coordinator this way; Polly and Orca's coordinators
  keep their tools.

## Consequences

- The laptop needs the `hypomnema` binary; `hypomnema login` and `hypomnema skills install` set it up.
- A test checks that every command in the skill parses with this binary's flags, and that a changed stub records its
  digest, so later installs can update it.
- Authorization stays in Go behind the client API whichever door a client uses.

## Sources

[docs/research/2026-11-09-coordinator-cli-and-skill.md](../research/2026-11-09-coordinator-cli-and-skill.md).
