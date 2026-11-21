# Anthropic's public repositories as references

Date: 2026-11-21. Asked by the owner for [issue #1045](https://github.com/kl3inIT/hypomnema/issues/1045): what
Anthropic's own repositories (https://github.com/anthropics) show about setting up skills, plugins, unattended
Claude Code, long-running agents and sandboxes, against what Hypomnema does. Research only; each change gets its
own issue.

Clones under `.tmp/anthropics-<name>` (shallow, 2026-11-21):

| Group | Repository | Commit | Licence |
|---|---|---|---|
| Skills | `skills` | `e4bb5d9` | Apache-2.0 per skill (`LICENSE.txt`); `doc-coauthoring` has none |
| Plugins | `claude-plugins-official` | `73c8cf3` | Apache-2.0 |
| | `financial-services` | `3e32c22` | Apache-2.0 |
| | `claude-for-legal` | `4a6c651` | Apache-2.0 |
| | `life-sciences` | `b39023a` | Apache-2.0 per skill |
| | `healthcare` | `8cefe4c` | no licence file |
| | `launch-your-agent` | `d5d0ffb` | Apache-2.0 |
| | `knowledge-work-plugins` (`.tmp/knowledge-work-plugins`) | `e91a134` | root `LICENSE` |
| Unattended Claude Code | `claude-code-action` | `acfe799` | MIT |
| | `claude-code-base-action` | `74af7ec` | MIT (mirror) |
| | `claude-code-security-review` | `0c6a49f` | MIT |
| | `code-migration-kit-with-claude-code` | `cf91c9d` | Apache-2.0 |
| | `claude-code` | `26a5b52` | proprietary: examples read, not copied |
| Agents and loops | `claude-agent-sdk-python` | `3184360` | `LICENSE` |
| | `claude-agent-sdk-typescript` | `1e66742` | `LICENSE.md` (no SDK source, changelog only) |
| | `claude-agent-sdk-demos` | `826b268` | no licence file |
| | `single-agent-loop-kit` | `c437962` | Apache-2.0 |
| | `cwc-long-running-agents` | `ad107a9` | `LICENSE` |
| | `claude-quickstarts` | `4a4224b` | `LICENSE` |
| | `commerce-agents` | `fd4d592` | `LICENSE` |
| Sandbox and security | `sandbox-runtime` | `0bbebed` | Apache-2.0 |
| | `defending-code-reference-harness` | `d3bea6b` | Apache-2.0 |
| | `oss-scanner` | `8f580fe` | Apache-2.0 |

## 1. Skills and plugins

- **Skill format** (`skills/template/SKILL.md:1-4`, `skills/skill-creator/SKILL.md`): `name` and `description`
  required, `license` on most; body under 500 lines, then `scripts/`, `references/`, `assets/` (progressive
  disclosure, `skill-creator/SKILL.md:86-96`). Hypomnema's `skillpack` already checks the same limits
  (`internal/shared/skillpack/skillpack.go:60-75`).
- **Descriptions** are the only trigger and should be "a little bit pushy", list trigger phrases and say what not
  to use it for (`skill-creator/SKILL.md:67`; the `docx` and `pdf` descriptions).
- **Skill authoring with evals**: `skill-creator` writes `evals/evals.json`, runs with and without the skill, and
  tunes the description on 20 trigger queries split 60/40 train and held-out (`skill-creator/SKILL.md:335-404`).
- **19 skills**; general ones for a Bot: `pdf`, `docx`, `xlsx`, `pptx`, `doc-coauthoring`, `skill-creator`. The
  `document-skills` bundle (xlsx, docx, pptx, pdf) is the only grouping close to a default set.
- **Plugins** (`claude-plugins-official/README.md`): `.claude-plugin/plugin.json` (`name`, `version`,
  `description`, `author`), `skills/` (commands are the legacy form, `plugins/example-plugin/commands/example-command.md:6-8`),
  `agents/`, `hooks/hooks.json`, `.mcp.json`. Role plugins in `knowledge-work-plugins` mostly hold 5–10 skills and an
  `.mcp.json` of hosted connectors, naming connector categories (`~~chat`) in `CONNECTORS.md`.
- **Setup interview** (`claude-for-legal/commercial-legal/CLAUDE.md:3-12`, `skills/cold-start-interview`): a role
  plugin's skills stop and ask until an interview has written the user's profile.
- **Hypomnema today**: a Claude Code worker gets a `--plugin-dir` holding only `plugin.json` and `skills/`
  (`internal/node/skills/skills.go:177-195`); omp and opencode get a skills folder
  (`internal/node/acp/agents.go`); a Bot gets `--skill` from `hypomnema skills sync`
  (`deploy/coordinator/hypomnema-pi`). Built-in skills: `pdf`, `slides`, `pages`, `google-workspace`, `lark`,
  `aws`, `superset`, `android-device`, `github`, `gitlab` (`internal/controlplane/clientcli/skills.go:135`). The
  [pi defaults research](2026-11-14-pi-defaults.md) left Office and `skill-creator` waiting and did not read this
  repository.

## 2. Claude Code run unattended

- `claude-code-action` drives the Agent SDK's `query()` with the CLI's flags (`base-action/src/parse-sdk-options.ts:352`):
  `--model`, `--max-turns`, `--allowedTools`/`--disallowedTools`, `--mcp-config` (merged), `fallbackModel`,
  `appendSystemPrompt`, setting sources `user, project, local` by default (`:357-365`), permission mode `auto` in
  agent mode unless outsiders' text or a gateway `ANTHROPIC_BASE_URL` is involved (`src/modes/permission-mode.ts:1-45`).
- **The repository's own Claude config is untrusted**: `.claude/`, `.mcp.json` and `.claude.json` are replaced
  with the base branch's before the run, because hooks, env and `apiKeyHelper` from the working directory run
  before any permission check in non-interactive mode (`src/github/operations/restore-config.ts:299-325`).
  OpenClaw (`extensions/anthropic/cli-backend.ts:50`) and Paperclip (`claude-local/src/server/acp.ts:770`) pass
  `--setting-sources user` for the same exposure.
- **`CLAUDE_CODE_SUBPROCESS_ENV_SCRUB`** keeps secrets out of Bash children's environment (`action.yml:232-250,321`),
  described as best effort.
- **Result handling**: a `success` result with `is_error: true` is a failure; more turns than `maxTurns` is an
  error (`run-claude-sdk.ts:290-306`); the loop breaks after the `result` message because the iterator can stay open
  (`:243-257`). `claude-code-security-review` retries `error_during_execution` once and re-runs without the diff on
  "Prompt is too long" (`claudecode/github_action_audit.py:222-275`).
- **Long jobs** (`code-migration-kit`): phases with a prompt file each and a human gate; "done" means the output file
  exists, a manifest drives batches, two read-only reviewers per unit and a third on disagreement, commits by the
  orchestrator at batch boundaries (`CLAUDE.md:20-45`, `scripts/queue_runner.mjs`).
- **Settings examples** (`claude-code/examples/settings/settings-hipaa.json`, `settings-strict.json`): deny reading
  `~/.ssh`, `~/.aws`, `~/.config/gh`, credential files; `disableBypassPermissionsMode`.
- **Hypomnema's Claude Code worker** (`internal/node/claude/runner.go`): `--permission-mode manual` with every
  request allowed, `--strict-mcp-config`, Task/Agent/WebSearch/WebFetch denied, gateway credentials, the model's
  window (#1019), auto-memory and git instructions off; no `--setting-sources`, no env scrub, no turn cap.

## 3. Long-running agents and the Agent SDK

- **Fresh session, state in files** (`claude-quickstarts/autonomous-coding/agent.py`, `prompts/coding_prompt.md`):
  a new client per session; a feature list where only `passes` may change; orient with `git log` and a progress
  file; re-check one or two passing features before new work; end clean before the context fills.
- **Harness gates** (`cwc-long-running-agents/README.md`): a default-fail contract (a hook refuses writing results
  without reading evidence), a fresh-context evaluator with no write tools answering `PASS` or `NEEDS_WORK`, a
  `PROGRESS.md` handoff, `commit-on-stop.sh`, and operator kill and steer files.
- **Limits in the harness, not the prompt** (`single-agent-loop-kit/reference_loop/loop.py:14-18,50-59`): turn and
  token budgets (subagents counted), a write gate, distinct stop reasons; a subagent returns one fixed shape
  (`skill/templates/subagent_response.schema.json`).
- **Delegation** (`commerce-agents/commerce-common/commerce_common/delegation.py:5-60`, `docs/safety.md`): a delegate
  gets a brief, read tools, a wall-clock budget, and returns a schema-checked result; third-party text is fenced.
- **SDK defaults** (`claude-agent-sdk-python/src/.../types.py:2759-2767`, `subprocess_cli.py:525-526`): setting
  sources `None` loads all; `[]` isolates and is advised when `cwd` may be untrusted.
- **Hypomnema today**: a worker's prompt asks for `finish` exactly once with a summary and evidence
  (`internal/node/runner/prompt.go`); a handoff gives the last 40 transcript items and rules where files win
  (`internal/controlplane/orchestration/handoff.go:82`); the reviewer profile ends with a `Verdict` line
  (`migrations/00115_reviewer_runs_checks.sql`). Already the "fresh worker reads the files" idea; the result is prose.

## 4. Sandbox and security

- **`sandbox-runtime`** (Linux: bubblewrap with `--new-session --die-with-parent` and `--cap-drop ALL`,
  `src/sandbox/linux-sandbox-utils.ts:797,3611`; network through host proxies with a domain allow-list after
  `--unshare-net`; writes denied by default with mandatory denies for `.gitconfig`, `.bashrc`, `.mcp.json`,
  `.git/hooks`, `src/sandbox/sandbox-utils.ts:21-45`).
- **`defending-code-reference-harness`**: `vuln-scan` (read-only tools, one subagent per focus area) and `triage`
  (adversarial check by three votes, dedup by root cause, severity from preconditions; `docs/triage.md`); untrusted
  text wrapped in a nonce tag (`harness/prompts/untrusted.py:17-34`). Its ASAN and gVisor pipeline is C/C++ only.
- **Hypomnema today**: the Bot's box is bubblewrap without `--unshare-net` (the host's network, owner,
  2026-11-12), without `--cap-drop` or `--new-session` (`deploy/coordinator/hypomnema-pi`); workers run as the
  node's user with no sandbox (Claude Code allows every request; Codex `--dangerously-bypass-approvals-and-sandbox`).

## 5. Candidates for the owner

| # | Candidate | From | Where in Hypomnema |
|---|---|---|---|
| 1 | Office skills for Bots and workers: `docx`, `xlsx`, `pptx` | `anthropics-skills/skills/{docx,xlsx,pptx}` (Apache-2.0) | `internal/controlplane/clientcli/skills/`, the built-in list; fills the waiting Office row of the pi defaults research. `pptx` overlaps `slides` |
| 2 | Workers do not load a repository's own Claude config: `--setting-sources user` | `claude-code-action` `restore-config.ts:299-325`; OpenClaw `cli-backend.ts:50`; Paperclip `acp.ts:770` | `internal/node/claude/runner.go`; the "to decide" row of #1019 |
| 3 | `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` for Claude Code workers | `claude-code-action/action.yml:232-250,321` | `internal/node/claude/runner.go` env |
| 4 | `skill-creator` with trigger evals, and pushier descriptions for the built-in skills | `anthropics-skills/skills/skill-creator/SKILL.md:67,335-404` | built-in skills' `SKILL.md`; a store skill |
| 5 | A `security-reviewer` profile beside `reviewer` | `defending-code-reference-harness/.claude/skills/vuln-scan`, `docs/triage.md` | a profile and its seed migration |
| 6 | Bot box: `--cap-drop ALL`, `--new-session` | `sandbox-runtime` `linux-sandbox-utils.ts:797,3611` | `deploy/coordinator/hypomnema-pi`, `box_linux_test.go` |

Left for later: a schema for `finish`'s report and harness-level turn budgets (single-agent-loop-kit,
commerce-agents; one reference family each), a network allow-list for the Bot's box (`sandbox-runtime`; departs
from the owner's 2026-11-12 choice), filesystem confinement of workers (a change of the permissive-worker stance),
role plugins with hosted MCP connectors (the control plane provides tools and credentials), `healthcare` (no
licence).
