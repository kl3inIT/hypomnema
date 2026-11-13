// Adapted from Omnigent web/src/lib/toolTitle.ts and toolValue.ts at cf1021e (Apache License 2.0,
// https://github.com/omnigent-ai/omnigent; see web/THIRD_PARTY_NOTICES.md). Changed for Hypomnema: Omnigent's own
// sys_* tools and its shell output formats are left out, the worker's ask and finish tools are added, and
// hypomnema is the MCP server whose tools are read by their bare name.
//
// Titles for tool-call rows: a bold `verb` plus a dynamic `body` ("Read foo.py").
// Tools without a formatter get a humanized name and key argument values, with
// the MCP server as a tag and the raw name in the tooltip.

/**
 * Structured title for a tool call.
 * - `verb`: the static action phrase (e.g. "Read"). Rendered bold/foreground
 *   by the row. `null` when there's nothing to emphasize (raw shell commands).
 * - `body`: the dynamic payload (path, command). Empty string when the title
 *   is verb-only (e.g. "Update todos").
 * - `filePath`: set when the call targets exactly one file, as the agent
 *   passed it (relative or absolute).
 * - `isShellCommand`: set when `body` is a shell command line; the row
 *   renders it terminal-style (monospace).
 * - `server`: the MCP server that owns the tool (e.g. "linear"); the row
 *   shows it as a tag.
 * - `toolName`: the raw tool name, set when the title was derived from it
 *   so the row's tooltip can still show the exact name.
 */
export interface ToolTitle {
  verb: string | null
  body: string
  filePath?: string
  isShellCommand?: boolean
  server?: string
  toolName?: string
}

type ArgFormatter = (args: Record<string, unknown>) => ToolTitle | null

const FORMATTERS: Record<string, ArgFormatter> = {
  // The worker's own tools (internal/node/workertools).
  ask: (args) => {
    const text = asString(args.text)
    return text === null ? verbOnly("Ask") : { verb: "Ask:", body: firstLine(text) }
  },
  finish: (args) => {
    const summary = asString(args.summary)
    return summary === null ? verbOnly("Report") : { verb: "Report:", body: firstLine(summary) }
  },

  // Claude Code native harness tools — mirror the step lines its TUI
  // prints. Shell steps carry a model-written description ("List docs
  // directory") which the CLI surfaces; prefer it over the raw command.
  Bash: (args) => {
    const desc = asString(args.description)
    if (desc !== null) return { verb: null, body: desc }
    return shellTitle(asString(args.command))
  },
  Read: (args) => withPath("Read", args.file_path),
  Write: (args) => withPath("Write", args.file_path),
  Edit: (args) => withPath("Edit", args.file_path),
  MultiEdit: (args) => withPath("Edit", args.file_path),
  NotebookEdit: (args) => withPath("Edit", args.notebook_path),
  Grep: (args) => {
    const pattern = asString(args.pattern)
    return pattern === null ? null : { verb: "Search:", body: pattern }
  },
  Glob: (args) => {
    const pattern = asString(args.pattern)
    return pattern === null ? null : { verb: "Find files:", body: pattern }
  },
  WebSearch: (args) => {
    const q = asString(args.query)
    return q === null ? null : { verb: "Web search:", body: `"${q}"` }
  },
  WebFetch: (args) => {
    const url = asString(args.url)
    return url === null ? null : { verb: "Web fetch:", body: url }
  },
  TodoWrite: () => verbOnly("Update todos"),
  Task: (args) => {
    const desc = asString(args.description)
    return desc === null ? verbOnly("Run sub-agent") : { verb: "Sub-agent:", body: desc }
  },

  // Codex native harness tools. Codex wraps every command in a login
  // shell (`/bin/bash -lc '...'`); show the inner command like its TUI.
  shell: (args) => {
    const cmd = asString(args.command)
    return shellTitle(cmd === null ? null : unwrapShellCommand(cmd))
  },
  apply_patch: (args) => {
    const changes = Array.isArray(args.changes) ? args.changes : null
    if (changes === null || changes.length === 0) return null
    if (changes.length === 1) {
      const only = changes[0]
      const path = only !== null && typeof only === "object" ? Reflect.get(only, "path") : null
      const title = withPath("Edit", path)
      if (title !== null) return title
    }
    return { verb: "Edit", body: `${changes.length} files` }
  },

  // Pi / OpenCode native harness tools (lowercase names). OpenCode
  // passes `filePath`, Pi passes `path`.
  bash: (args) => shellTitle(asString(args.command)),
  read: (args) => withPath("Read", args.filePath ?? args.path),
  edit: (args) => withPath("Edit", args.filePath ?? args.path),
  write: (args) => withPath("Write", args.filePath ?? args.path),

  // Web tools.
  web_search: (args) => {
    const q = asString(args.query)
    return q === null ? null : { verb: "Web search:", body: `"${q}"` }
  },
  web_fetch: (args) => {
    const q = asString(args.query)
    if (q !== null) return { verb: "Web fetch:", body: `"${q}"` }
    const url = asString(args.url)
    if (url !== null) return { verb: "Web fetch:", body: url }
    return null
  },
  web_read: (args) => {
    const url = asString(args.url)
    return url === null ? null : { verb: "Web read:", body: url }
  },
}

/**
 * Compute the title shown in a tool-call trigger row. Tries the per-tool
 * formatter first; otherwise humanizes the tool name and lists its key
 * argument values, with no tool-specific knowledge.
 */
export function formatToolTitle(name: string, args: Record<string, unknown>): ToolTitle {
  const { server, tool } = splitToolName(name)
  const formatter = server === undefined ? ownValue(FORMATTERS, tool) : undefined
  if (formatter !== undefined) {
    const title = formatter(args)
    if (title !== null && (title.verb !== null || title.body.length > 0)) {
      return title
    }
  }
  return {
    verb: humanizeToolName(tool, server),
    body: summarizeArgs(args),
    toolName: name,
    ...(server === undefined ? {} : { server }),
  }
}

// `server__tool`, as Claude Code names MCP tools after an `mcp__` prefix and
// SDK harnesses name a spec-declared MCP server's tools without one.
const SERVER_TOOL_RE = /^([A-Za-z0-9](?:[\w-]*?[A-Za-z0-9])?)__([A-Za-z0-9][\s\S]*)$/

/**
 * Split an MCP tool name into its server and tool: `mcp__linear__get_issue`
 * and `linear__get_issue` -> `{ server: "linear", tool: "get_issue" }`.
 * Hypomnema's MCP server carries the worker's own tools, so
 * `mcp__hypomnema__ask` resolves as `ask`.
 */
export function splitToolName(name: string): { server?: string; tool: string } {
  const bare = name.startsWith("mcp__") ? name.slice("mcp__".length) : name
  const match = SERVER_TOOL_RE.exec(bare)
  if (match === null) return { tool: name }
  const [, server, tool] = match
  if (server === "hypomnema") return splitToolName(tool!)
  return { server: server!, tool: tool! }
}

// Verbs a tool name may end with (`sys_scheduled_task_create`); moved to the
// front so the label reads as an action ("Create scheduled task"). Words like
// "list" and "search" are left out: at the end of a name they are nouns.
const TRAILING_VERBS = new Set([
  "add",
  "cancel",
  "close",
  "create",
  "delete",
  "get",
  "launch",
  "read",
  "remove",
  "rename",
  "send",
  "set",
  "start",
  "stop",
  "update",
  "write",
])
// A name that already starts with one of these reads as an action as-is.
const LEADING_VERBS = new Set([
  ...TRAILING_VERBS,
  "apply",
  "check",
  "edit",
  "execute",
  "fetch",
  "find",
  "list",
  "load",
  "manage",
  "merge",
  "open",
  "poll",
  "post",
  "query",
  "reply",
  "resolve",
  "run",
  "search",
  "submit",
])

/**
 * A tool name as a sentence-case action label: `manage_todo_list` -> "Manage
 * todo list". Drops a `sys_` prefix and a repeat of the server name
 * (`slack_read_api_call` on the slack server -> "Read API call").
 */
export function humanizeToolName(tool: string, server?: string): string {
  let words = identifierWords(tool)
  if (words.length > 1 && words[0] === "sys") words = words.slice(1)
  const serverWords = server === undefined ? [] : identifierWords(server)
  let shared = 0
  while (shared < serverWords.length && shared < words.length - 1) {
    if (words[shared] !== serverWords[shared]) break
    shared += 1
  }
  words = words.slice(shared)
  const last = words[words.length - 1]
  if (
    words.length > 1 &&
    last !== undefined &&
    TRAILING_VERBS.has(last) &&
    !LEADING_VERBS.has(words[0]!)
  ) {
    words = [last, ...words.slice(0, -1)]
  }
  const label = sentenceCase(words)
  return label.length > 0 ? label : tool
}

const ARG_SUMMARY_PARTS = 3
const ARG_STRING_MAX_CHARS = 200
const ARG_LIST_MAX_ITEMS = 3
const ARG_LIST_ITEM_MAX_CHARS = 40
// Numbers only identify something under id-like keys; a bare `20` from
// `limit` or `max_results` means nothing in a one-line summary.
const ID_LIKE_KEY_WORDS = new Set(["id", "number", "num", "pid", "port"])
// Arguments that pick which fields or columns come back (`fields: [id, title]`,
// `select: "id,title"`); they shape the result rather than name what the call is about.
const SELECTOR_KEY_WORDS = new Set([
  "attributes",
  "columns",
  "expand",
  "fields",
  "properties",
  "select",
])
// Words that mark an argument as a credential; matched per word so `author` passes.
const SECRET_KEY_WORDS = new Set([
  "apikey",
  "auth",
  "authorization",
  "bearer",
  "cookie",
  "credential",
  "credentials",
  "key",
  "passphrase",
  "passwd",
  "password",
  "pat",
  "private",
  "secret",
  "signature",
  "token",
])

/**
 * Key argument values for a collapsed row, joined with " · "; skips long,
 * multi-line, nested and secret-looking values.
 */
export function summarizeArgs(args: Record<string, unknown>): string {
  const parts: string[] = []
  for (const [key, value] of Object.entries(args)) {
    if (parts.length >= ARG_SUMMARY_PARTS) break
    if (identifierWords(key).some((word) => SECRET_KEY_WORDS.has(word))) continue
    const part = argSummaryPart(key, value)
    if (part !== null) parts.push(part)
  }
  return parts.join(" · ")
}

function argSummaryPart(key: string, value: unknown): string | null {
  const lastWord = identifierWords(key).at(-1)
  if (lastWord !== undefined && SELECTOR_KEY_WORDS.has(lastWord)) return null
  if (typeof value === "string") {
    const text = value.trim()
    if (text.length === 0 || text.length > ARG_STRING_MAX_CHARS || text.includes("\n")) return null
    return text
  }
  if (typeof value === "number") {
    return lastWord !== undefined && ID_LIKE_KEY_WORDS.has(lastWord) ? String(value) : null
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return null
    const shortStrings =
      value.length <= ARG_LIST_MAX_ITEMS &&
      value.every((item) => typeof item === "string" && item.length <= ARG_LIST_ITEM_MAX_CHARS)
    if (shortStrings) return value.join(", ")
    return `${value.length} ${value.length === 1 ? "item" : "items"}`
  }
  return null
}

/**
 * Action categories for the collapsed tool-run summary line. Names cover
 * the native harness CLIs (Claude Code's Bash/Read/Edit..., Codex's
 * shell/apply_patch, pi's lowercase tools). Shell calls whose
 * command is a bare `ls` re-categorize as directory listings, matching
 * the Claude Code TUI's step summaries.
 */
export type RunCategory = "shell" | "list" | "read" | "edit" | "search"

/** One tool call in a folded run: its name plus (optional) arguments. */
export interface ToolRunCall {
  name: string
  args?: Record<string, unknown>
}

const RUN_CATEGORIES: Record<string, RunCategory> = {
  Bash: "shell",
  shell: "shell",
  local_shell: "shell",
  bash: "shell",
  Read: "read",
  read: "read",
  Edit: "edit",
  Write: "edit",
  MultiEdit: "edit",
  NotebookEdit: "edit",
  apply_patch: "edit",
  edit: "edit",
  write: "edit",
  patch: "edit",
  Grep: "search",
  Glob: "search",
  WebSearch: "search",
  web_search: "search",
  grep: "search",
  glob: "search",
  list: "list",
}

const RUN_CATEGORY_ORDER: readonly RunCategory[] = ["shell", "list", "read", "edit", "search"]

function runPhrase(category: RunCategory, n: number): string {
  const s = n === 1 ? "" : "s"
  switch (category) {
    case "shell":
      return `ran ${n} shell command${s}`
    case "list":
      return `listed ${n} director${n === 1 ? "y" : "ies"}`
    case "read":
      return `read ${n} file${s}`
    case "edit":
      return `edited ${n} file${s}`
    case "search":
      return `ran ${n} search${n === 1 ? "" : "es"}`
  }
}

/** A tool name without Hypomnema's `mcp__hypomnema__` prefix. */
export function stripOwnPrefix(name: string): string {
  return name.startsWith("mcp__hypomnema__") ? name.slice("mcp__hypomnema__".length) : name
}

// Login-shell wrapper Codex puts around every command, e.g.
// `/bin/bash -lc 'cat notes.txt'` or `/bin/zsh -lc ls`.
const SHELL_WRAPPER_RE = /^(?:\S+\/)?(?:bash|zsh|sh|dash|fish)\s+-l?c\s+([\s\S]+)$/

/**
 * Peel a login-shell wrapper (and its quoting) off a command string. Anything
 * after the wrapper that is not exactly one shell word, e.g.
 * `sh -c 'a' && echo 'b'`, is returned unchanged.
 */
function unwrapShellCommand(command: string): string {
  const trimmed = command.trim()
  const match = SHELL_WRAPPER_RE.exec(trimmed)
  if (match === null) return trimmed
  const inner = decodeShellWord(match[1]!.trim())?.trim()
  return inner ? inner : trimmed
}

/** Decode one shell word (`'it'\''s'`, `"a \"b\""`, `ls`), or `null` if it is not exactly one. */
function decodeShellWord(text: string): string | null {
  const piece = /'([^']*)'|"((?:[^"\\]|\\[\s\S])*)"|\\([\s\S])|([^\s'"\\]+)/y
  let decoded = ""
  while (piece.lastIndex < text.length) {
    const match = piece.exec(text)
    if (match === null) return null
    const [, singleQuoted, doubleQuoted, escaped, bare] = match
    if (doubleQuoted !== undefined) {
      decoded += doubleQuoted.replace(/\\([\\"$`])/g, "$1")
    } else {
      decoded += singleQuoted ?? escaped ?? bare
    }
  }
  return decoded
}

/** Whether `name` is a shell-command tool in any harness (Bash, shell, bash). */
export function isShellTool(name: string): boolean {
  return ownValue(RUN_CATEGORIES, stripOwnPrefix(name)) === "shell"
}

// Leading `NAME=value` assignments, with quoted values, e.g.
// `PATH="$HOME/bin:$PATH" FOO=1 make` -> `PATH="$HOME/bin:$PATH" FOO=1 `.
// Unquoted values stop at quotes, parens and shell operators, so neither
// `FOO="a b"c` nor `FOO=a;b` is split inside a value.
const ENV_PREFIX_RE =
  /^(?:[A-Za-z_][A-Za-z0-9_]*=(?:"(?:[^"\\]|\\.)*"|'[^']*'|[^\s"'()`;&|<>]*)\s+)+/
const ASSIGNMENT_RE = /^[A-Za-z_][A-Za-z0-9_]*=/

/**
 * Split leading environment assignments off a command so the row can dim
 * them and let the program name stand out. `env` keeps its trailing
 * whitespace; `env + rest` always equals the input.
 */
export function splitEnvPrefix(command: string): { env: string; rest: string } {
  const match = ENV_PREFIX_RE.exec(command)
  if (match === null) return { env: "", rest: command }
  const rest = command.slice(match[0].length)
  // Assignments with no program after them (`FOO=1 BAR=2`) are not a prefix.
  if (rest.length === 0 || ASSIGNMENT_RE.test(rest)) return { env: "", rest: command }
  return { env: match[0], rest }
}

/**
 * A script's first line for a collapsed row, and how many more non-blank lines
 * follow it. Leading comment lines are skipped: the first line is always top
 * level, so a `#` there starts a comment.
 */
export function shellCommandPreview(script: string): { firstLine: string; moreLines: number } {
  const lines = script
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "")
  const shown = lines.find((line) => !line.startsWith("#")) ?? lines[0] ?? ""
  return { firstLine: shown.replace(/\s*\\$/, ""), moreLines: Math.max(0, lines.length - 1) }
}

/** The action category of a tool call, e.g. `Bash("ls")` -> "list". */
export function toolCallCategory(call: ToolRunCall): RunCategory | "other" {
  const name = stripOwnPrefix(call.name)
  const category = ownValue(RUN_CATEGORIES, name) ?? "other"
  if (category === "shell") {
    const raw = typeof call.args?.command === "string" ? call.args.command : ""
    const command = unwrapShellCommand(raw)
    if (command === "ls" || command.startsWith("ls ")) return "list"
    if (command === "cat" || command.startsWith("cat ")) return "read"
  }
  return category
}

/**
 * Label for the folded (hidden) part of a tool run, mirroring the
 * semantic one-liners the native CLIs print: "Read 2 files", "Ran 1
 * shell command, read 2 files". Runs made up entirely of unrecognized
 * tools fall back to a generic "Called N tools".
 */
export function formatToolRunLabel(calls: ToolRunCall[]): string {
  const counts = new Map<RunCategory | "other", number>()
  for (const call of calls) {
    const category = toolCallCategory(call)
    counts.set(category, (counts.get(category) ?? 0) + 1)
  }

  const known = RUN_CATEGORY_ORDER.filter((c) => counts.has(c))
  const other = counts.get("other") ?? 0

  if (known.length === 0) {
    return `Called ${calls.length} tool${calls.length === 1 ? "" : "s"}`
  }

  const phrases = known.map((c) => runPhrase(c, counts.get(c)!))
  if (other > 0) {
    phrases.push(`called ${other} other tool${other === 1 ? "" : "s"}`)
  }
  const label = phrases.join(", ")
  return label.charAt(0).toUpperCase() + label.slice(1)
}

function asString(v: unknown): string | null {
  return typeof v === "string" && v.length > 0 ? v : null
}

function shellTitle(command: string | null): ToolTitle | null {
  return command === null ? null : { verb: null, body: command, isShellCommand: true }
}

function verbOnly(verb: string): ToolTitle {
  return { verb, body: "" }
}

function withPath(verb: string, raw: unknown): ToolTitle | null {
  const path = asString(raw)
  return path === null ? null : { verb, body: path, filePath: path }
}

/**
 * The command line a shell tool call ran, or `null` when the call is not a
 * shell tool or carries no string `command`. Only Codex's `shell` adds a
 * login-shell wrapper, so other harnesses' commands are shown as run.
 */
export function shellCommand(name: string, args: Record<string, unknown>): string | null {
  if (!isShellTool(name)) return null
  const command = asString(args.command)
  if (command === null) return null
  return stripOwnPrefix(name) === "shell" ? unwrapShellCommand(command) : command
}

function firstLine(text: string): string {
  return text.trim().split("\n")[0] ?? ""
}

const ACRONYMS: Record<string, string> = {
  ai: "AI",
  api: "API",
  cli: "CLI",
  csv: "CSV",
  db: "DB",
  html: "HTML",
  http: "HTTP",
  id: "ID",
  ids: "IDs",
  json: "JSON",
  llm: "LLM",
  mcp: "MCP",
  os: "OS",
  pdf: "PDF",
  pr: "PR",
  prs: "PRs",
  sdk: "SDK",
  sql: "SQL",
  ssh: "SSH",
  ui: "UI",
  uri: "URI",
  url: "URL",
  urls: "URLs",
}

/** Lowercased words of a snake_case, kebab-case, dotted, or camelCase identifier. */
function identifierWords(identifier: string): string[] {
  return identifier
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .split(/[\s_.-]+/)
    .filter((word) => word.length > 0)
    .map((word) => word.toLowerCase())
}

/** `record[key]` for an own property only, so payload keys like `constructor` miss. */
function ownValue<T>(record: Readonly<Record<string, T>>, key: string): T | undefined {
  return Object.hasOwn(record, key) ? record[key] : undefined
}

/** Words as a sentence-case label: `["next", "run", "at"]` -> "Next run at". */
function sentenceCase(words: string[]): string {
  return words
    .map((word, index) => {
      const acronym = ownValue(ACRONYMS, word)
      if (acronym !== undefined) return acronym
      return index === 0 ? word.charAt(0).toUpperCase() + word.slice(1) : word
    })
    .join(" ")
}
