# How a coordinator drives orchestration: CLI and skill, or MCP

Date: 2026-11-09. Read from the clones in `.tmp/` (paths relative to each clone) and the official skill docs. Inferences
are marked.

The question: issue #41 first proposed an MCP server for coordinators (pi on the server, the owner's Claude Code or
Codex on a laptop). The owner asked whether a CLI is better, since MCP tool definitions cost tokens on every turn,
and that pi keep its own tools. This note compares what the references do and how they word the skill that teaches
an agent to coordinate.

## 1. CLI or MCP

| Reference | How an outside agent drives it | Notes |
|---|---|---|
| Orca | CLI plus a skill: `orca orchestration … --json` | MCP only to attach outside tools to agents (`docs/site/content/docs/cli/skills.mdx` "MCP servers") |
| genie | CLI plus skills (`genie task`, `genie board`) | Had an MCP server and retired it in v5, removing even the stub in v6 (`UPGRADING.md` "MCP retirement"); "zero-daemon … every command is fork-and-exit" (`CLAUDE.md:227`) |
| Omnara | Remote MCP at `/mcp`, a CLI and an SDK | A hosted platform serving clients without a shell |
| OpenClaw | `openclaw mcp serve`, a CLI and ACP | Many client kinds; its own coordinator uses in-process tools |
| Coder | `coder exp mcp server` (stdio) and a remote MCP server (`docs/mcp-server.md`) | Same reason |
| Omnigent | An MCP relay into Claude Code (`harnesses/claude_native/bridge.py`) | Puts its tools into a vendor CLI it does not control; its coordinator Polly uses its own tools |
| Hermes | CLI; serving MCP is an optional plugin | |

The pattern: where the coordinator has a shell, the references use a CLI and a skill; MCP is added as another door
for clients without a shell.

Token cost (pi `docs/mcp.md`, "Control tool exposure"): a server with `direct` exposure declares every tool to the
model on every request; `deferred` and `codemode` load them on demand at the cost of a search step. A skill costs
only its description until it is used. *Inference, not measured*: about 18 orchestration tools would add a few
thousand tokens per request with `direct` exposure.

Authorization does not depend on the door: the CLI calls the client API with a token, so every check stays in Go.

**Decided by the owner, 2026-11-09:** coordinators use the `hypomnema` CLI and a coordinator skill, as Orca's do. pi
keeps its own tools and edits no code. MCP is added later only for a client without a shell.

## 2. Orca's skill, the closest reference

- **A thin stub, a guide in the binary.** `skills/orchestration/SKILL.md` (62 lines) resolves the executable and runs
  `orca skills get orchestration`, which prints the guide compiled into that binary (`src/cli/bundled-skill-guides.ts`),
  so the guide always matches the commands. The stub's frontmatter is copied byte for byte from the guide by the
  generator (`config/scripts/skills/bundle-generation/generate-bundled-skill-guides.mjs:102`). Rare branches live in
  `references/*.md`, fetched with `skills get orchestration --reference <file>`.
- **The description** names the outcome ("Run work in separate Orca worker agents and get their results back"), the
  triggers ("Use when the user asks for Orca workers or supervision …"), resolves one ambiguous request outright, routes
  the worker audience by its signature ("a prompt with Task and Dispatch IDs"), and ends with a negative trigger and a
  sibling: "Otherwise prefer your own subagents, including for long or parallel work. Use `orca-cli` to hand work off
  with nothing expected back".
- **The guide** (`skill-guides/orchestration.md`, 114 lines): worker branch first; Recipe; a `Never:` list; "When Orca
  wakes you"; Task spec; Safety; a situation → reference table. Key sentences:
  - "Start every independent worker, then **end your turn**. "Report back" and "tell me when it's done" still mean end
    your turn; Orca wakes you and you report then."
  - "Never: Poll or run watch scripts … Orca wakes you. … Make checkouts yourself; let `worker-start` create a
    worker's workspace."
  - "Answer a `question` … when the answer follows from the task or the user's instructions; otherwise ask the user
    first."
  - "A `worker_done` settles its Task; never mark a reported failure completed."
  - "When every worker has settled, report to the user per Task: outcome, evidence, open blockers."
  - "Make each spec self-contained: the target, the change, constraints, what the worker may edit, and the evidence
    that proves it is done."
  - "Silence, a timeout, or `unverifiable` liveness alone never proves exit or authorizes autonomous recovery."
  - "If a receipt has `next`, follow it." The CLI's output carries the next step, not the prose.
  - From `references/recovery-and-cleanup.md`: "Act only on proof." "Retry a failed or stopped attempt on the same
    Task." After three failed starts, "Report the failure and stop automatic retries; never route around the breaker".
- **Techniques:** imperative short sentences; each ban carries its reason ("a script's plain `check` takes your mail,
  so it is hidden from you"); one copy-paste recipe per action with `<placeholders>`; errors handled by code, one
  action each; "do not guess unsupported commands", use `--help`.
- **Install:** `orca skills install` writes the stub to `~/.agents/skills/<name>` (read by Codex and others) and links
  it into `~/.claude/skills`; `update` rewrites only copies whose digest matches a shipped release and skips edited
  ones (`src/main/skills/bundled-skill-install/classify.ts`).
- **Lessons:** the first shipped skill (kept as a test fixture,
  `src/main/skills/bundled-skill-install/orchestration-v1.3.24-rc.2.skill-md.txt`, 196 lines) had a keyword-list
  description and taught `check --wait` loops; the current one reverses both. Tests pin wording: every command in a
  guide may use only flags the CLI accepts (`src/cli/specs/bundled-guide-flags.test.ts`); the preamble's shell block
  must parse (`preamble.test.ts`). Source comments: "Place behavior rules beside CLI examples, where agent readers use
  them" (`src/main/runtime/orchestration/preamble.ts:51`).

## 3. Other references

- **genie** `skills/work/SKILL.md` (86 lines), the coordinator skill over `genie task`:
  - "Give each worker its goal, deliverables, criteria, validation, dependencies, owned files, relevant context, and
    stop conditions."
  - "Rich text and JSON leaving through a shell CLI … is payload, not syntax, so it travels through a file or standard
    input … a zero exit proves the command ran, not that the text survived."
  - "Stop and ask only for an irreversible or destructive action, a security-sensitive decision, a side effect
    outside the worktree, or a plan so broken that every path forward is a guess. Everything else is a ruling".
  - "a worker notification is not delivery evidence by itself."
  - Its `skills/authoring/SKILL.md`: the description "front-loads the verb and names the outcome … every word costs on
    every turn"; forty to ninety lines, rare branches in `references/`; "Prompt the positive"; "Every step ends on a
    checkable completion criterion"; "a line that restates a command's help output is a cache that will go stale".
- **Omnigent Polly** (`examples/polly/config.yaml`): "you do NOT write code"; "Act in the SAME turn you announce … a
  turn whose entire content is an intent sentence with no tool call is a dropped turn"; "Waiting on sub-agents is
  the framework's job, not yours"; "Do not infer success from git status alone". `skills/fanout/SKILL.md`: "Dispatch
  the whole parallel-safe set, THEN (and only then) END YOUR TURN. Do not poll."
- **Hermes** kanban orchestrator (`agent/prompt_builder.py:263-330`): "your job is routing, not implementation";
  "Design decisions belong to you, the orchestrator, not to workers … Every child card body must carry the decisions
  it depends on, because workers cannot see sibling context." `dynamic-workflow` pitfalls: trusting
  "`status=completed` for completion".
- **OpenClaw** (`src/agents/system-prompt.ts:670-708`, `delegation-guidance.ts:118-145`): "Delegate a bounded,
  independent task only when parallel execution or an independent review provides a concrete benefit. Keep dependent
  steps with the same owner." "brief each child with objective, output, write scope, verification." "A child run
  ending does not end the user's delegated goal. Compare its result with the requested outcome". "Never loop-poll".
- **Omnara** example preamble (`examples/sre-agent/SKILL.md:17-34`): "Stop only where a step says to ask the user …
  Where a step gives a default, use it without asking and say in a line what you picked"; "add `--json` to read IDs".

## 4. Official guidance

- Agent Skills specification (agentskills.io/specification): `name` 1–64 characters `a-z0-9-`, matching the
  directory; `description` 1–1024 characters, "what the skill does and when to use it", with keywords; body under
  5000 tokens; references one level deep. Claude Code, pi and Codex all read this format.
- Anthropic's best practices (platform.claude.com …/agent-skills/best-practices): third-person description with
  triggers; "Claude is already very smart. Only add context Claude doesn't already have"; low freedom ("exact
  instructions") for fragile sequences; one default with an escape hatch instead of options; checklists and
  validate-then-proceed loops; one term per concept; no time-sensitive text; evaluations before polishing, tested on
  every model that will run it.
- skill-creator: descriptions slightly "pushy" against under-triggering; explain why instead of all-caps MUST/NEVER;
  trigger evals with near-miss negatives. Codex's skill-creator adds: exclusions "only when they prevent likely
  misrouting".
- Locations: Claude Code `~/.claude/skills/<name>/`; pi `~/.pi/agent/skills/` and `~/.agents/skills/`; Codex
  `~/.agents/skills/`. pi lists only name, description and path, and the model reads the file when the task matches
  (`src/core/skills.ts:358`); `/skill:name` forces it. Codex requires the whole `SKILL.md` read before acting
  (`codex-rs/ext/skills/src/catalog_prompt.rs`).

## 5. What this means for Hypomnema (inference)

- Copy Orca's shape: a thin stub that runs `hypomnema skills get coordinator`; a short guide compiled into the binary;
  rare branches (recovery, retries) as references; `skills install` into `~/.agents/skills` and `~/.claude/skills`,
  updating only unedited copies.
- Commands are exact, with `--json`; long text goes through a file or standard input; the CLI's output names the next
  step.
- Waiting: pi is woken by the control plane (issue #41, PR 3), so it ends its turn as Orca's coordinators do. An
  agent on the laptop cannot be woken from outside; it runs one blocking `hypomnema` wait command, in the background
  where the agent supports being notified when a command exits, instead of a polling loop.
- Wording: description verb-first with triggers and one negative ("otherwise do the work yourself"); rules beside the
  commands they govern, each with its reason; positive phrasing where one exists; a test that every command in the
  guide parses.
