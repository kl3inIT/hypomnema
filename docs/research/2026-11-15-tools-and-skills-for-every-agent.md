# Tools and skills for every agent kind: how the references do it

Date: 2026-11-15. Read-only study for Hypomnema at `main` 2ecbc96. The owner's decision is at the end.

Revisions read (clones under `D:\Workspaces\hypomnema\.tmp\`): OpenMausBot `a6cd67c78`, pi `827383a15` (pi-coding-agent
1.5.2, the version `deploy/coordinator/package.json` pins), pi-web-access 0.48.0 (`pipkgs/nicobailon_pi-web-access`,
896d75b), omp `57263720c8`, Hermes `dc965932a0`, OpenClaw `7b3c6edf5`, Codex `c2c1ebba`, opencode, Omnigent, Omnara.
No Claude Code docs clone exists; Claude Code facts come from how OpenMausBot, OpenClaw and Omnigent launch it.
Some reference facts were read by sub-agents; the ones the recommendation rests on (OpenMausBot, OpenClaw bundle MCP,
Hermes tools MCP server, Codex image_gen gate, pi MCP) were read directly.

## 0. What Hypomnema has today (read in code)

| Capability | Bot (pi in the box) | Claude Code worker | Codex worker | omp / opencode worker |
|---|---|---|---|---|
| Model | gateway | gateway (`ANTHROPIC_BASE_URL`, `internal/node/claude/runner.go:186`) | gateway (`-c` provider) | gateway |
| Web search | `hypomnema web search` via bash, taught by built-in skill `web-search` | Anthropic's own WebSearch (server tool executed by Anthropic behind the gateway's passthrough), not the gateway's search chain | native `web.run`, answered by the gateway's chain on `/v1/alpha/search` | MCP `web_search` from the worker tools server; native off (`web_search.enabled: false`; opencode `websearch` denied) |
| Web fetch | `hypomnema web fetch` | Claude Code's WebFetch | MCP `web_fetch` | MCP `web_fetch`; native off |
| Images | `hypomnema image` via bash, skill `images` | none | none (Codex's `image_gen` needs OpenAI auth, see 2) | native `generate_image` / `image_generate` pointed at the gateway (`internal/node/acp/agents.go:71-134`); but omp's `generate_image.enabled` defaults to false (`.tmp/omp/packages/coding-agent/src/tools/settings.ts:654-656`) and Hypomnema's omp config does not set it, so omp workers likely have no image tool (inference from the code, not seen on a node) |
| Browser, memory, ask/finish | CLI | MCP (worker tools server) | MCP | MCP |
| Built-in skills (pdf, pages, web-search, images, coordinator) | `~/.agents/skills`, installed by `setup-coordinator.sh` (`clientcli/skills.go`) | never | never | never |
| Owner's skills (store, revisions) | `skills sync --bot` into `run/<chat>/skills`, `--skill` | plugin dir (`--plugin-dir`) | `CODEX_HOME/skills` | agent dir / config `skills` |
| Integration guides | `~/.agents/skills` stubs | `$HYPOMNEMA_SKILLS` (`internal/shared/agenttool/setup.go`) | same | same |

Already in place: one MCP server per worker, `internal/node/workertools`, whose `tools/list` is what the control
plane's agent API lists (`internal/controlplane/agentapi/web.go:17-35`, `webToolsOf` by runtime), so a tool added on
the control plane reaches workers without a node release. The agent API already accepts a Bot pi's token
(`agentapi.go:192-198`). Bots do not mount it: "Not built: MCP servers, pi extensions" (ARCHITECTURE.md:680).

## 1. Tools vs skills in each reference

| Ref | Tool | Skill | Who gets which; on/off | Path |
|---|---|---|---|---|
| OpenMausBot | a callable on one harness MCP server, `agents` (JSON-RPC, no SDK), plus browser/computer/data servers | a folder with SKILL.md; a prompt index line "Read <path>" | tools: `CatalogProfile` from `OMB_*` env switches, "everything is off unless the harness says 1" (`imageGeneration`, `research`, `memoryEnabled`…), plus per-bot Tool selection allow/exclude (`native:read`, `mcp:mail:*`), "take effect on the next turn"; skills: per-bot manifest + library with `everyBot`, `defaultEnabled`, `triggerTerms` | `server/drivers/agents-mcp.ts`, `agents-catalog.ts:951-996`, `agents-catalog-profile.ts`, `docs/tool-selection.md`, `server/skills.ts:1255`, `server/skill-library.ts:20-28` |
| OpenClaw | "Tools are callable actions, skills teach agents how to work, plugins add runtime capabilities such as tools, providers…" | SKILL.md, gated on `requires.{bins,env,config}` | `tools.profile` (minimal/coding/messaging/full) + `tools.allow/alsoAllow/deny`, `tools.byProvider`, `agents.entries.*.tools`; `skills.allowBundled`, `agents.entries.*.skills` | `docs/tools/index.md`, `docs/gateway/config-tools/tool-policy.md`, `docs/tools/skills-config.md` |
| Hermes | registry entry with `check_fn`; grouped in toolsets | SKILL.md, reached by `skills_list`/`skill_view` tools and an `<available_skills>` index | `platform_toolsets.<platform>`; `skills.disabled`, `skills.platform_disabled.<platform>`; subagents get a narrower set (`DELEGATE_BLOCKED_TOOLS`) | `tools/registry.py`, `toolsets.py`, `agent/skill_utils.py:289`, `tools/delegate_tool_toolsets.py` |
| pi | `pi.registerTool({...})` in an extension; built-in MCP tools `mcp__<server>__<tool>` | `~/.pi/agent/skills`, `~/.agents/skills`, `.pi/skills`, `--skill <path>` | `--tools/--exclude-tools/--no-tools`, `defaultTools`; `skills` setting, `--no-skills` | `docs/extensions.md:1582`, `docs/mcp.md`, `docs/settings.md:42,157`, `src/core/resource-loader.ts:1023` |
| omp | built-ins with `<tool>.enabled` keys (`web_search`, `fetch`, `generate_image` …) | many sources (`.agents/skills`, Claude, Codex…) | `config.yml` per tool; `skills.*` settings, `disabledExtensions: ["skill:x"]` | `src/tools/settings.ts`, `docs/skills.md` |
| opencode | built-ins incl. `websearch`, `webfetch` | `.opencode/skills`, `.claude/skills`, `.agents/skills` | `permission` (allow/ask/deny, wildcards), per-agent `tools`/`permission` | `tools.mdx`, `agents.mdx`, `skills.mdx` |
| Codex | built-ins + hosted (`web_search` mode, `image_gen`) + MCP | `.agents/skills`, `$CODEX_HOME/skills/.system` (5 bundled) | `web_search = disabled|cached|live`, `features.image_generation`; `[skills] bundled.enabled`, `[[skills.config]] enabled` | `core/src/config/mod.rs:2827`, `features/src/lib.rs:2033`, `ext/skills/src/host_roots.rs`, `config/src/skills_config.rs` |
| Omnigent | its tools over its own `serve-mcp` relay | bundle `skills/` | Claude `--plugin-dir`, Codex `$CODEX_HOME/skills` mirror, `skills_filter` | `harnesses/claude_native/bridge.py:3103-3215`, `harnesses/codex_native/app_server.py:355-390`, `inner/bundle_skills.py` |

Common shape (all eight): a tool is code the agent calls, enabled by an allow/deny list per agent or profile and
enforced where it runs; a skill is a document read on demand, enabled per agent, and never a substitute for a tool.
No reference teaches a web or image tool with a skill that drives a CLI through bash; pi's own example only says
"use brave-search skill via bash" (`examples/extensions/plan-mode/index.ts:223`).

## 2. Web search, fetch and images; one central service; MCP as the bridge

| Ref | Web search / fetch | Images | Central service | Exposure to the CLIs it drives |
|---|---|---|---|---|
| OpenMausBot | engine's own (Claude WebSearch, Codex native); pi has none unless the owner adds an MCP server (`docs/custom-mcp-servers.md:223`, You.com example); paid research via `agents` MCP → `/api/internal/research/*` | `generate_image` on the `agents` MCP → `/api/internal/images/generate`, shown only when `imageGeneration` is on | its own server (internal routes) for its tools | one `agents` MCP server mounted into every engine: pi via its extension `-e pi-mcp-extension.ts` with `OMB_MCP_CONFIG` (its pi then had no MCP), Codex `-c mcp_servers.<name>.*` (`codex-mcp-mount.ts`), Claude `--mcp-config --strict-mcp-config` (`claude-project-mcp.ts`), ACP agents `session/new` `mcpServers`, stdio or HTTP (`turn-mcp.ts:95-121`) |
| OpenClaw | `web_search`, `web_fetch` with provider plugins (`registerWebSearchProvider`), `tools.web.search.provider` | `image_generate`, `imageGenerationProviders` | the Gateway process | "A backend with `bundleMcp: true` receives Gateway tools through a loopback MCP bridge" with a per-run grant `OPENCLAW_MCP_TOKEN` (`docs/gateway/cli-backends.md:808-830`, `src/gateway/mcp-http*.ts`); Claude's WebSearch is turned off (`injectClaudeWebSearchDisabledArgs`, `--disallowedTools WebSearch`) exactly when a provider is configured (`bundle-mcp.ts:65-77`); Codex keeps its own search answered by the gateway |
| Hermes | `web_search`, `web_extract`; vendors in `plugins/web/<vendor>`; `web.backend: nous` routes through the managed Tool Gateway | `image_generate`, `plugins/image_gen/*`, `image_gen.provider: nous` | Nous Tool Gateway (`tools/managed_tool_gateway.py`) | for its Codex runtime a stdio MCP server exposing `web_search, web_extract, vision_analyze, image_generate, skill_view, skills_list, text_to_speech` (`agent/transports/hermes_tools_mcp_server.py:57-59`), registered as `[mcp_servers.hermes-tools]` |
| Omnigent | engines' own | engines' own | — | own `serve-mcp` relay: Claude `--mcp-config`, Codex `[mcp_servers.omnigent]` |
| omp | `web_search` chain (`modelRoles.web`), fetch in `read` | `generate_image` (`modelRoles.image`) | none; custom base URLs | MCP client: `~/.omp/agent/mcp.json`, also imports `.claude`, `.codex`, `opencode.json` |
| opencode | `websearch` (Exa/Parallel MCP; only with OpenCode provider or `OPENCODE_ENABLE_EXA`), `webfetch` | experimental image tool | none | MCP: `"mcp": {name: {type: local|remote …}}` |
| Codex | hosted `web_search` (mode) | hosted `image_gen`, offered only when the feature is on, the plan is not Free, the provider has `image_generation`, the model takes images, and auth is OpenAI/Codex backend (`core/src/tools/spec_plan.rs:821-856`) — so not under a custom gateway provider | — | `mcp_servers.<name>` in config.toml or `-c` |
| pi-web-access | `web_search`, `fetch_content`, … many backends with `*BaseUrl` overrides; also ships `pi-web-access-mcp` | — | — | pi extension, or its own MCP bin |

Every reference that drives other CLIs (OpenMausBot, OpenClaw, Hermes for its Codex runtime, Omnigent), and
Hypomnema's own worker tools, exposes their own tools to Claude Code and Codex as one MCP server of theirs. Every CLI here mounts MCP:
Claude Code `--mcp-config` (+`--strict-mcp-config`), Codex `mcp_servers`, omp `mcp.json` / ACP `mcpServers`,
opencode `mcp` / ACP, pi 1.5.2 built-in `~/.pi/agent/mcp.json` (stdio and streamable HTTP; `pi mcp`, `/reload`
re-reads it; `docs/mcp.md`). Only OpenClaw and Hermes route search and images through one central service of
their own; OpenClaw is the one that also switches the CLI's native search off when its own is configured.

## 3. Lifecycle

OpenMausBot in detail (read in code):
- Install: the harness ships the `agents` server inside its release (`agents-proxy`, `agents-mcp.ts`); nothing is
  installed into the engine. Bundled skills live in `skills/` with a manifest (`defaultEnabled`, `triggerTerms`,
  `requiredCapabilities`); `mergeSkills(bundled, user)` (`skill-library.ts:108`).
- Update: a new app release changes the catalog; the list is computed per turn from env switches
  (`agentsToolList(env)`), so no engine config is rewritten.
- Per-bot on/off: tools by `OMB_*` switches (presentation) and Tool selection (enforced by an MCP gate,
  `gateServer`, and server-side checks: "Presentation only: the server still authenticates and scopes every
  call"); skills by the bot's manifest and the library's `everyBot`/`enabled` (`registry-config.ts:193-200`).
- Removal: a switched-off skill's links are removed from `.claude/skills`, `.agents/skills`, `.grok/skills`
  (`skills.ts:336,382`, "links, not copies"); a tool off is not listed and refused.
- How a running agent learns: each turn builds the MCP config and the skills index afresh (pi: the MCP file is
  "written on every turn", `pi.ts:465`; Tool selection "take effect on the next turn").
- Skills reach every engine the same way: links into each engine's native skill folder plus one prompt index
  ("Before starting a task one of these covers, read its exact SKILL.md path", `skills.ts:1255-1290`).

Others: OpenClaw snapshots skills at session start, rescans on file watch or install, "existing sessions use
changed snapshots on their next agent turn"; its tool catalog cache lives 30 s; when Claude Code reports the
`openclaw` MCP server not connected it fails the turn and retires the process. Hermes seeds bundled skills by a
hash manifest (`tools/skills_sync.py`), skips user-edited ones, and pushes mid-conversation skill changes
(`agent/skills_index_delta.py`). Codex writes system skills at start (`install_system_skills`). pi: `/reload`
re-reads skills, extensions and `mcp.json`; a newly registered tool is live at once.

## 4. Bundled skills for every agent kind

- Codex: its 5 system skills to every session. OpenClaw: bundled skills to every agent, Claude CLI runs included
  (`src/agents/cli-runner/claude-skills-plugin.ts`, plugin `openclaw-skills`), narrowed by `allowBundled` and per
  agent. OpenMausBot: the same library skills for every engine (links + index), per bot. Omnigent: bundle skills
  to Claude (`--plugin-dir`) and Codex (`$CODEX_HOME/skills`) alike. Hermes: subagents get a narrower toolset;
  skills follow the profile.
- So the common default is: bundled skills go to every agent kind, chat or coding, by the same switch, with a role
  skill (an orchestrator's) given only to that role. Hypomnema's Bot-only built-ins are the departure.

## 5. Options for exposing tools, by complexity

| Option | Moving parts | Failure modes | What must be operated | References |
|---|---|---|---|---|
| A. Native tool per CLI, each pointed at the gateway | one config dialect per CLI (omp `models.yml`/`config.yml`, opencode permissions, Codex `-c` flags, Claude none possible for search) | a CLI release renames a key or changes its provider logic; Claude Code and Codex images cannot be pointed at all | per-CLI tests at each CLI upgrade; a node release for each change | omp/opencode images today; Codex `web.run` (OpenClaw does the same) |
| B. One Hypomnema MCP server, list served by the control plane | one server (exists: `workertools`), one list (`agentapi`) | server fails to start → no tools (OpenClaw: fail the turn) | nothing new per CLI; a tool change is a control-plane release | OpenMausBot, OpenClaw, Hermes, Omnigent, Hypomnema workers |
| C. pi extension | one TypeScript extension | only pi; a second implementation beside B | pi version upgrades | OpenMausBot before pi had MCP |

B has the fewest moving parts that must stay consistent; A remains only where the CLI's native tool already calls
the gateway and nothing in B would be gained.

## 6. Recommended standard (for the owner to decide)

| # | Decision | From |
|---|---|---|
| 1 | One backend: the gateway's services (search chain, fetch, Images) answer every web and image call from every agent kind, recorded on the caller's key. | Hermes Tool Gateway, OpenClaw Gateway providers, OpenMausBot `/api/internal/*`; Hypomnema today |
| 2 | One exposure: the Hypomnema MCP server (today's `workertools`, list from the agent API) is the way a Hypomnema tool reaches any agent; `web_search`, `web_fetch`, `generate_image` become tools on it for every runtime. | OpenMausBot `agents` server, OpenClaw bundle MCP, Hermes `hermes-tools`, Omnigent `serve-mcp` |
| 3 | Bots mount the same server: pi 1.5.2's built-in MCP, an `mcp.json` in the private agent dir bound read-only (as `settings.json` is), the server run with the chat pi's token, which the agent API already accepts. The exact command (a `hypomnema mcp` subcommand reading `client.json`) is inference. | OpenMausBot (pi gets the `agents` server), pi `docs/mcp.md` |
| 4 | Rule for a CLI's own tool: off when Hypomnema serves the same capability, kept only when the gateway itself answers it. So Codex keeps `web.run` (answered on `/v1/alpha/search`); omp and opencode keep their native image tool pointed at the gateway, or switch to the MCP `generate_image` (owner's choice; both meet the rule). | OpenClaw `resolveCliNativeWebSearchEnabled` (native off when a provider is set) and its Codex search setting |
| 5 | Claude Code: `--disallowedTools WebSearch,WebFetch` and the MCP `web_search`/`web_fetch`, since its WebSearch is executed by Anthropic, not by the gateway's chain. This reverses the owner's choice of 2026-11-13 and needs the owner's decision. | OpenClaw `injectClaudeWebSearchDisabledArgs` when a provider is configured |
| 6 | Codex and Claude Code images: MCP `generate_image` (Codex's `image_gen` is offered only with OpenAI/Codex auth, `spec_plan.rs:821-856`, so it cannot be pointed at the gateway). | Hermes exposes `image_generate` to Codex over MCP; OpenMausBot `generate_image` |
| 7 | The `web-search` and `images` skills are removed once Bots have the tools; usage notes (send an image with `MEDIA:<path>`, untrusted content) move into the tool's description and result. The `hypomnema web|image` commands stay for the owner and Routine scripts, which have no MCP (inference). | No reference teaches a tool with a skill; OpenMausBot's `generate_image` description says the image "is attached to your reply" |
| 8 | Built-in skills (pdf, pages) are ordinary skills for every agent kind: published into the skills store as a built-in revision at each release, with "for everyone" on, and placed by the existing Dispatch/Bot path; `hypomnema-coordinator` stays a role skill for the coordinator. | Codex system skills, OpenClaw `allowBundled` + Claude skills plugin, OpenMausBot library `everyBot`; Hermes `skills_sync` manifest for updates |
| 9 | Integration guides become skills placed by the same path when the Connection is given, instead of the separate `$HYPOMNEMA_SKILLS` folder. | OpenClaw `requires.env/bins` gating; OpenMausBot `requiredCapabilities` |
| 10 | Switches: a profile's YAML and a Bot get `tools: [...]` / `tools_off: [...]` beside `skills`/`skills_off`, by tool name; the control plane lists and also refuses a tool that is off. | OpenClaw `agents.entries.*.tools` allow/deny, OpenMausBot Tool selection + server checks, Hermes `platform_toolsets` |
| 11 | Lifecycle: tools change with a control-plane release (no node release; the list is read at each `tools/list`); skills change by store revision; a running agent sees both at its next process start (worker turn, pi start for a chat). No reliance on MCP `list_changed` (no reference relies on it; inference). | OpenMausBot per-turn config, OpenClaw "next agent turn", Hypomnema's `dispatch_skills` |
| 12 | Failure: if the Hypomnema MCP server is not connected at start, the turn fails and the process is not reused. | OpenClaw `cli-backends.md:813-818` |

Inference, not in any reference: the `hypomnema mcp` command for Bots; keeping `hypomnema web|image` for Routine
scripts; choosing between omp/opencode native image tools and MCP `generate_image` (both fit row 4).

## 7. Decision

The owner approved the standard in section 6, including row 5: Claude Code's own WebSearch and WebFetch are turned
off and it searches through the Hypomnema MCP server, reversing the choice of 2026-11-13 (owner, 2026-11-15). The
design and plan are in a GitHub issue; the open choice in row 4 (omp and opencode images) is settled there.
