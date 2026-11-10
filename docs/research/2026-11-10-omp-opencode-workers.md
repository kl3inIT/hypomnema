# omp and opencode as workers

Date: 2026-11-10. For the issue "omp and opencode as workers through the model gateway". The owner wants a Task to be
able to run under omp or opencode, with the model coming from the gateway (so an omp worker can use the Antigravity
account while a Claude Code worker keeps its own subscription), and both CLIs installed on a node by the control
plane, not by hand.

Read by four Sonnet scouts in the clones under `.tmp/`: omp at 660a31d, opencode at 145e948 (OpenCode 2, CLI package
`@opencode/cli` 2.2.5; its user docs still describe version 1), and omnigent, openhands with its SDK, orca, coder,
omnara, openclaw and hermes-agent for how they run several agent CLIs. Paths are in those repositories. The fourth
scout mapped Hypomnema's own node at `origin/main`.

## 1. How the references run several agent CLIs

| | CLIs | Interface | One runner or one per CLI | Model source | Who installs the CLI | Isolation between runs |
|---|---|---|---|---|---|---|
| OpenHands (SDK) | Claude Code, Codex, Gemini CLI, pi, opencode | ACP for all | One `ACPAgent` and a registry of providers (`openhands/sdk/agent/acp_agent.py` L1832, `settings/acp_providers.py`) | An environment variable per provider; pi and opencode need their own config file for a base URL | Pinned npm packages baked into the image, else `npx` on first use (`settings/acp_install_catalog.py`) | A container; a data directory per conversation only on request |
| Coder | Any ACP adapter the user declares | ACP | One generic client in Go (`agent/x/agentacp/`, about 2500 lines) | Its gateway docs: `opencode.json` `baseURL` for opencode | Registry modules or the image; the adapter by the user | A workspace per container |
| OpenClaw | Claude, Codex, opencode, pi and others | ACP through `acpx`; Codex's app-server | One ACP backend, agents as data (`extensions/acpx`) | The CLI's own login | `npx` on first use | A separate `CODEX_HOME`; otherwise shared |
| Orca | opencode, omp, grok, antigravity over ACP; Claude by SDK; Codex by app-server | ACP, SDK, app-server | `AcpLaunchSpec` as data, one connection, a small dialect file per CLI (`src/main/acp/acp-launch-specs.ts`, `acp-dialects/omp-dialect.ts` 136 lines, `opencode-dialect.ts` 108) | The user's own CLI config | Expects it installed; checks a minimum version at every launch | A config directory per account (`PI_CODING_AGENT_DIR`, `XDG_*`) |
| Omnigent | Claude, Codex, pi, opencode and a dozen more | Each CLI's own protocol (pi `--mode rpc`, `opencode serve`), and ACP for new ones | One class per CLI behind `Executor` (`omnigent/inner/executor.py` L657): pi 3474 lines, opencode about 7000; a table for ACP CLIs (`acp_cli_harnesses.py`) | A config file written per run: pi `models.json` and `opencode.json` with the gateway's base URL and a token (`pi_executor.py` L828, `harnesses/opencode_native/provider.py` L57) | `npm install -g` at a pinned version from a setup step, with a version range; outside it the harness is "unavailable" (`onboarding/harness_install.py`) | A config directory per session and an environment of allowed variables only (`_clean_pi_env`) |

Omnara and Hermes run no other agent's CLI.

- **Interface.** Four of five use the Agent Client Protocol with one generic client; both `omp acp` and
  `opencode acp` exist and Orca runs both that way. Only Omnigent writes a runner per CLI.
- **Gateway.** Only Omnigent points pi and opencode at its own gateway, by writing their config file for each run.
- **Extra tools.** OpenHands and Omnigent pass their MCP server in ACP's `session/new`. Orca and Coder pass none.
- **Install.** Nobody installs on the control plane's instruction. Omnigent comes closest: a pinned install and a
  version gate.
- **Isolation.** None gives a run its own `HOME` by default; the CLI's own variable is pointed at a directory.
  omp's own supervisor, `python/robomp`, does set `HOME` per worker (`src/worker.py` L244-246).

## 2. omp without a terminal

- **Modes** (`docs/rpc.md`, `README.md` L539-563): `--mode rpc` (JSON lines on stdio, with a Go client in
  `sdk/go/omp-rpc/`), `-p --mode json` (one shot), and `omp acp`.
- **Approvals**: the default `tools.approvalMode` is `yolo`: nothing is asked (`docs/settings.md` L632).
- **Gateway**: `<agent dir>/models.yml` declares a provider with `baseUrl`, `api: openai-completions`, `apiKey` (an
  environment variable's name, a literal or `!command`) and models; `--model provider/id` picks one
  (`docs/models.md` L18-100, L319-395).
- **State**: `PI_CODING_AGENT_DIR` moves the agent directory; `--session-dir` the sessions. omp also reads
  `~/.claude`, `~/.codex`, `~/.gemini` and the project's `.omp/` and `.claude/` (`docs/config-usage.md` L88).
- **MCP**: `<agent dir>/mcp.json`; a tool is `mcp__<server>_<tool>`. By default MCP tools are not plain tools but
  mounted under `xd://`; `tools.xdev: false` makes them plain (`docs/settings.md` L643, `docs/rpc.md` L549-563).
- **Usage**: every assistant message carries `usage` with tokens and cost; `get_session_stats` gives totals.
- **Install**: it needs Bun, or the standalone binary: `curl -fsSL https://omp.sh/install | sh -s -- --binary --ref
  v<version>`, checked by SHA-256 (`README.md` L39-100).

## 3. opencode without a terminal

- **Modes** (`packages/cli/src/commands/commands.ts` L400-451, `handlers/acp.ts`): `opencode run --format json`
  (one process per turn; the exit code is the result), `opencode serve` (HTTP and SSE) and `opencode acp`.
- **Approvals**: the default agent allows everything but reading `.env` files; `"permission": "allow"` allows those
  too (`packages/schema/src/agent.ts` L44-51).
- **Gateway**: `opencode.json` with a provider of `npm: "@ai-sdk/openai-compatible"`, `options.baseURL` and
  `options.apiKey`; that package is built in, so nothing is downloaded (`packages/core/src/aisdk-native.ts` L69).
  `OPENCODE_CONFIG_CONTENT` carries the whole config in the environment.
- **State**: the four `XDG_*` directories; `OPENCODE_DISABLE_AUTOUPDATE`, `OPENCODE_DISABLE_MODELS_FETCH`,
  `OPENCODE_DISABLE_FREE_MODELS`.
- **MCP**: `mcp.<name>` with `type: "local"` and a command, or `mcpServers` in ACP's `session/new`. By default MCP
  tools are reachable only through one `execute` tool ("code mode"); `codemode: false` makes them plain, named
  `<server>_<tool>` (`packages/core/src/tool.ts` L260-265).
- **Usage**: tokens and cost at each step's end and with the prompt's answer in ACP (`acp/translate.ts` L150-166).
- **Headers**: every model request carries `x-opencode-session-id` and `x-session-affinity`
  (`session/model-request.ts` L312-327).
- **Install**: `@opencode/cli-<os>-<arch>` from the npm registry, a single binary, put in `~/.opencode/bin` by
  the install script (`install` L195-212).

## 4. Hypomnema's node today

- The daemon takes exactly one `Runner` (`internal/node/daemon/daemon.go` L64-81, L118) and `node.Run` gives it
  Claude Code's (`internal/node/node.go` L193-196). An offer's runtime is never looked at.
- The worker's tools `ask` and `finish` are an MCP server of the node's own (`hypomnema-node mcp --dir …`) over a
  mailbox of files; any runner can attach it.
- The only credential that travels with an offer is the git host's token (`nodeapi.go` L359-364); a gateway key per
  Dispatch is stated in the glossary and not built.
- Placement needs an account for the runtime (`placement.go` L209).
- CLIs are installed only on a machine created at a provider, by its first-boot script, unpinned, and a failure is
  only logged (`machines/firstboot.sh.tmpl` L15-20).

## 5. Read for Hypomnema

- One ACP runner with omp and opencode as two entries is what most references do, and Orca's two dialect files show
  how little differs. What each CLI needs beside the protocol is known: the gateway in its own config file, its MCP
  tools made plain, its state in a directory of the run's.
- Writing the CLI's config for each run with the gateway's address and a key is Omnigent's way, and the key per
  Dispatch is what the glossary already says.
- Installing on the control plane's instruction is the owner's rule and goes further than any reference; Omnigent's
  pinned version and its "unavailable" outside the range are the nearest pattern.
