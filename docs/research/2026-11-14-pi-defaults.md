# pi's defaults for a Bot: skills, tools, settings and community packages

Date: 2026-11-14. Asked by the owner for issue #366: which skills a Bot's pi gets by default, which built-in tools
and settings of other agents are worth taking, and which community pi packages are good. Nothing here is decided.

Read by three sub-agents in the clones under `.tmp/`, revisions: pi `827383a15`, OpenClaw `7b3c6edf5`, Hermes
`dc965932a0`, OpenMausBot `6ae15f252`, Omnigent `2641a4a`, omp `57263720c8`, opencode `6205e99c`, Codex `c2c1ebba`;
community packages from the npm registry (keyword `pi-package`, about 13,700 packages) and their GitHub
repositories, cloned under `.tmp/pipkgs/`. What a Bot's pi has today is in issue #366.

## 1. Skills the references ship on by default

| Product | Bundled | Default | Licence |
|---|---|---|---|
| OpenClaw | 50 in `skills/` (+4 custodian) | on when its binary or credential is there; `skills.allowBundled` narrows (`docs/tools/skills-config.md:280`); `coding-agent` opt-in | MIT; `skill-creator` Apache-2.0 |
| Hermes | 44 in `skills/`, 141 in `optional-skills/` | `skills/` on; a profile may keep only `hermes-agent` (`agent/skill_utils.py:278`) | MIT |
| OpenMausBot | 6 in `skills/` | `defaultEnabled`, loaded into a turn only when its trigger words match (`server/skill-library.ts:116`) | Apache-2.0 |
| Codex | 5 system skills in the binary | all, written to `$CODEX_HOME/skills/.system` (`codex-rs/skills/src/lib.rs:58-100`) | Apache-2.0, each with its `LICENSE.txt` |
| Omnigent | examples only (`examples/deep-research`, `examples/polly`) | none | Apache-2.0 |
| omp, pi, opencode | none general (opencode: Figma plugin skills) | — | MIT |

Kinds that suit a Bot, with who ships them:

| Kind | Where | Note |
|---|---|---|
| Skill creation | Codex `codex-rs/skills/src/assets/samples/skill-creator`, OpenClaw `skills/skill-creator`, OpenMausBot `skills/create-verification-skill` | the most common default (3 products) |
| Google Workspace, daily brief | Hermes `skills/productivity/google-workspace`, OpenClaw `skills/gog` | ours would go through the service proxy |
| Mail triage (read and draft, never send or delete) | Hermes `skills/email/email-inbox-triage` | |
| Weekly review | Hermes `skills/productivity/weekly-review-planning` | written to run on a schedule: a Routine |
| PDF; docx, pptx, xlsx | Hermes `skills/productivity/pdf`, `skills/productivity/microsoft-office` (scripts, own MIT licence); OpenClaw `nano-pdf` | need Python libraries in the box |
| Meeting or document to action items | Hermes `skills/productivity/meeting-action-items`, `document-to-action-items` | |
| Cited research; a blocked page | Hermes `skills/research/grounded-citations`, `skills/web/blocked-page-recovery`; Omnigent `examples/deep-research` | |
| Watching news or feeds | Hermes `competitor-news-monitor`, OpenClaw and Hermes `blogwatcher` | as Routines |
| GitHub | Hermes, OpenClaw (on when `gh` is there) | the coordinator skill covers Tasks |

None ships a memory or a scheduling skill: both are tools (OpenClaw's memory tool, Hermes's cron).

## 2. Built-in tools pi lacks

| Tool | References | Hypomnema |
|---|---|---|
| Web search, fetch | 5 and 4 refs: omp `tools/settings.ts:876`, `tools/fetch.ts`; opencode `packages/core/src/tool/plugin/websearch.ts`, `webfetch.ts`; OpenClaw `src/agents/tools/web-search.ts`; Hermes `tools/web_tools.py`; Codex hosted | `hypomnema web search|fetch` through the control plane (#354) |
| Ask the user | 6 refs: omp `tools/ask.ts`, opencode `plugin/question.ts`, OpenClaw `tools/ask-user-tool.ts`, Hermes `tools/clarify_tool.py`, Codex `handlers/request_user_input.rs`, OpenMausBot's pi extension (`server/drivers/pi.ts:465`, answered over `extension_ui_request`) | none for pi |
| Todo or plan | 4 refs: omp `tools/todo.ts`, opencode `plugin/todo.ts`, Hermes `tools/todo_tool.py`, Codex `handlers/plan.rs`; pi's own example `examples/extensions/todo.ts` | #366 item 2 |
| Subagent | 5 refs: omp `task/`, opencode `plugin/subagent.ts`, OpenClaw `sessions_spawn`, Hermes `tools/delegate_tool.py`, Codex `handlers/multi_agents.rs`; pi's example `examples/extensions/subagent/` | #366 item 5 |
| Wait, background jobs | 4 refs: omp `tools/wait.ts`, OpenClaw `agents_wait`, Hermes `process_registry*.py`, Codex `unified_exec.rs` | #366 item 4 |
| grep, find, ls | 4 refs on; pi has them built in but off (`src/core/tools/{grep,find,ls}.ts`, `docs/settings.md:46`) | off |
| Memory | OpenClaw `extensions/memory-core`, Hermes `tools/memory_tool.py`; omp off by default | the Bot's memory in the control plane (`hypomnema memory`) |
| Schedule | opencode `plugin/schedule.ts`, OpenClaw `tools/cron-tool.ts`, Hermes `tools/cronjob_tools.py` | Routines through the CLI |
| Send a message | OpenClaw `message`, Hermes `tools/send_message_tool.py`, Codex `send_message_to_user_async.rs` | none |

pi's built-in `codemode` and `tool_search` are loaded but inactive (`src/extensions/codemode/`, `tool-search/`).

## 3. Settings

| Setting | pi default | Others |
|---|---|---|
| Compaction trigger | 16,384 free tokens, fixed (`docs/settings.md:74`) | omp max(15% of the window, 16,384) (`packages/agent/src/compaction/compaction.ts:194,329`); opencode 15% free, at least 16k (`packages/core/src/session/compaction.ts:129,135`); Hermes at 50% (`cli-config.yaml.example:727`); Codex at 90% (`codex-rs/protocol/src/openai_models.rs:582`); OpenClaw at least 20,000 (`src/agents/agent-settings.ts:12`) |
| Recent tokens kept | 20,000 | omp 20,000 (`session/context-settings.ts:283`); opencode 15,000 |
| Thinking | `medium` | Hermes `medium`; omp `high`; Codex per model |
| Tool output | 2,000 lines / 50 KB (`src/core/tools/truncate.ts:11`) | opencode and Hermes the same; omp 3,000 / 50 KB |
| Retry | 3 tries, 2 s to 60 s | opencode 3; Codex 4-5; omp 10 with a fallback model |
| Turn limit | none | Hermes 500; others none or per agent |

## 4. Community pi packages

pi now has MCP built in (`docs/mcp.md`). In `--mode rpc` an extension's `select`, `confirm` and `input` reach the
client; custom components, footer and editor do nothing (`docs/rpc-extension-ui.md`).

| Package | Adds | Stars, licence, last publish | For a Bot |
|---|---|---|---|
| pi-web-access (nicobailon) | `web_search` (Exa public MCP without a key), `fetch_content`, SSRF guard | 1.8k, MIT, 2026-11-05 | overlaps `hypomnema web`, and sends queries from the box to Exa directly |
| pi-memory (jayzeng) | markdown memory in `~/.pi/agent/memory`, once per session | 218, MIT, 2026-08-11 | overlaps the control plane's memory |
| @sting8k/pi-vcc | compaction without a model call, and `vcc_recall` after it | 589, MIT, 2026-10-29 | a candidate for #366 item 1, beside omp's methods |
| cc-safety-net (kenryu42) | blocks destructive shell commands and secret-file reads, fails closed | 2.0k, MIT, 2026-11-12 | the box already bounds pi |
| @gotgenes/pi-permission-system | allow, ask, deny rules; ask goes over RPC | 280, MIT, 2026-10-28 | overlaps the box and Approvals |
| pi-subagents (nicobailon) | subagents, chains, background runs | 4.2k, MIT, 2026-11-13 | about 112k lines; pi's own example is about 1.4k |
| pi-mcp-adapter (nicobailon) | lazy MCP servers, tool search | 1.6k, MIT, 2026-11-04 | turns pi's built-in MCP off |
| badlogic/pi-skills | Gmail, Calendar, Drive CLIs, Brave search, transcripts | 2.7k, MIT, 2026-06-06 | their own OAuth, not the service proxy |

Risks: download counts are inflated (about 80 near-identical `@agimon-ai/po-*` packages, BUSL-1.1); `billion-context`
is a proxy that rewrites the conversation; `context-mode` is Elastic-2.0; many packages are TUI-only. Every package
runs with the agent's rights (`docs/packages.md`): pin an exact version and read its code first.

## 5. What the comparison suggests (inference, for the owner to decide)

1. Settings: switch on `grep`, `find`, `ls`; a compaction trigger that scales with the window, as omp and opencode;
   the rest as pi's defaults, which match most references.
2. Skills on by default, copied with their licences: skill creation (Codex), PDF and Office files, mail triage,
   weekly review, action items, cited research (Hermes), each changed to use Hypomnema's tools.
3. Extensions from pi's own examples (todo, subagent) rather than large community packages; light compaction
   (#366 item 1) to be chosen between omp's methods and pi-vcc.
4. Not taken: community packages that duplicate the control plane (web search, memory, permissions, MCP adapter).

## 6. The owner's choices (2026-11-14)

- Settings (section 5, item 1): built as issue #395.
- Skills: the PDF skill now. Dashboards and diagrams first, as artifacts the owner sees in the chat, after a study
  of how the references make and show them. The others wait here and get an issue when one is needed; the owner
  named `meeting-action-items` for a later meeting feature.

The candidates, read in their `SKILL.md`, so that an issue can start from here:

| Skill | Source | Does | Needs | Status |
|---|---|---|---|---|
| `skill-creator` | Codex `codex-rs/skills/src/assets/samples/skill-creator` (Apache-2.0, 9 files); OpenClaw, OpenMausBot | the Bot writes or fixes a skill | its "Codex skill" wording changed; a skill the Bot writes waits for the owner | waiting |
| `pdf` | Hermes `skills/productivity/pdf` (MIT, 21 files with scripts) | create, read, merge, fill, OCR PDFs | Python with `pypdf`, `pdfplumber`, `reportlab`, `pypdfium2` (pinned in its Prerequisites) in the Bot's box | **chosen** |
| `microsoft-office` | Hermes `skills/productivity/microsoft-office` (MIT, 30 files) | create, read, edit docx, pptx, xlsx | the same Python libraries as `pdf` | waiting |
| `email-inbox-triage` | Hermes `skills/email/email-inbox-triage` (MIT) | rank threads, draft replies; never send or delete by itself | Gmail through `gws` and the service proxy | waiting |
| `weekly-review-planning` | Hermes `skills/productivity/weekly-review-planning` (MIT) | weekly review: promises, slipping work, next week | calendar, mail, Runs; a Routine | waiting |
| `meeting-action-items` | Hermes `skills/productivity/meeting-action-items` (MIT) | meeting notes to cited decisions, owners and tasks | — | waiting, for the meeting feature (owner) |
| `document-to-action-items` | Hermes `skills/productivity/document-to-action-items` (MIT) | deadlines and obligations from documents, proposed for approval | — | waiting |
| `grounded-citations` | Hermes `skills/research/grounded-citations` (MIT, with scripts) | numbered sources, quotes checked | `hypomnema web` | waiting |
| `blocked-page-recovery` | Hermes `skills/web/blocked-page-recovery` (MIT) | a page that refuses a fetch: Wayback, archive.today | some steps need keys (Jina) | waiting |
| news or feed watch | Hermes `competitor-news-monitor`, OpenClaw and Hermes `blogwatcher` | watch sites on a schedule | an RSS tool | waiting |
| dashboards, diagrams | OpenMausBot `skills/data-dashboards`, OpenClaw `skills/visualize`, `skills/diagram-maker` | pages and diagrams | a way to show HTML in the chat | **next, as artifacts** |
| `google-workspace` | Hermes, OpenClaw `gog` | Gmail, Calendar, Drive | — | not taken: the Bots' `gws` guide covers it |
