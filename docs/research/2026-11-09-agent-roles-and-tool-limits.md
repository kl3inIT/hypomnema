# Agent roles and tool limits in the references

Date: 2026-11-09. Question: which roles do the references ship, how is one defined, and how is "read only" kept?
Read for issue #42 (profiles) after it merged. Code read in `.tmp/`.

## Roles and their definition

| Reference | Roles shipped | Defined by | "Read only" kept by |
|---|---|---|---|
| omp (oh-my-pi, a fork of pi) | `task` (full), `sonic` (mechanical, cheap model), `scout` (exploration), `reviewer`, `security-reviewer`, `seance`, `init` (`packages/coding-agent/src/prompts/agents/*.md`, `src/task/agents.ts`) | Markdown with frontmatter: `name`, `description`, `tools`, `spawns`, `model` (a role alias such as `@smol`, `@slow`, `@task`), `thinking-level`; the body is the system prompt (`docs/task-agent-discovery.md`). Role aliases map to models in `config.yml` `modelRoles` | The tool list: `scout` has `read, find, grep, glob, ast_grep, web_search`; `reviewer` has no edit or write tool (it keeps `bash`) |
| Omnara | None; the owner makes profiles (examples: "Support triage", `research-agent`) (`docs/agents/configuration.mdx`) | YAML: `instruction`, `model {provider_config, name, reasoning.effort}`, `tools` with a permission each, `mcp`, `skills`, `subagents` (another profile, with a `description`), `max_depth`. Configs are immutable; a profile points at the current one | Tool permissions (`always_ask`, `enabled: false`) |
| Polly (Omnigent) | `args.purpose`: `implement`, `review`, `explore` / `search` (`examples/polly/config.yaml:129-136`) | A dispatch argument, described in the coordinator's config | The instruction: the reviewer "reports issues, never edits"; explore is "read-only investigation" |
| genie | `engineer`, `reviewer`, `fix` (`skills/work/SKILL.md`) | Roles in the plan, rules in the skill | The instruction: "Reviewers remain read-only" |
| OpenClaw | Named agents | Config, with layered tool policy (`tools.allow`, `tools.deny`, `docs/tools/subagents/tool-policy.md`) | Tool policy |

pi itself ships no roles or subagents by design ("Pi ships with powerful defaults but skips features like sub-agents
and plan mode"); it is extended with TypeScript extensions and packages (`.tmp/pi/packages/coding-agent/docs/packages.md`,
`examples/extensions/subagent/`, `permission-gate.ts`, `protected-paths.ts`).

## What Hypomnema does (2026-11-09)

- Profiles `implementer`, `reviewer`, `explorer`, `coordinator`: the names match the references' common set (a doer,
  `reviewer`, an explorer).
- A profile has no `description`, so a coordinator chooses by name only.
- "Read only" is an instruction only. The node already answers Claude Code's permission prompts
  (`--permission-mode manual --permission-prompt-tool stdio`, `internal/node/claude/runner.go`) and allows every one,
  and passes `--disallowedTools AskUserQuestion`.

## Inferences

- Add `description` to profiles, as omp and Omnara's subagents have, and show it in `profiles list`.
- Keep "read only" in the runtime, as omp, Omnara and OpenClaw do: a profile names the tools its worker may use, and
  the node enforces it. Polly and genie rely on instructions; whether they enforce it elsewhere was not checked.
- omp's model role aliases (`@smol`, `@slow`) change the model of several roles in one place; not needed yet.
