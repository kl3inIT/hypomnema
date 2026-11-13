import type { TranscriptItem } from "@/api/generated/types.gen"

import {
  formatToolRunLabel,
  formatToolTitle,
  shellCommand,
  shellCommandPreview,
  splitToolName,
  toolCallCategory,
  type ToolTitle,
} from "./tool-title"

// A worker's transcript as it is drawn (issue #43). The references agree on the shape
// (docs/research/2026-11-10-worker-sessions.md): what the worker says is prose, and its work between two
// messages is one line that counts it in words and opens into one row a call. The rows follow Omnigent
// (web/src/components/blocks/ToolCard.tsx, BlockRenderer.tsx at cf1021e), whose titles and run labels are
// adapted in tool-title.ts, and Orca (src/renderer/src/components/native-chat/NativeChatToolRun.tsx,
// NativeChatToolLine.tsx at 4c13ae46): a shell row is its command, an edit carries its line counts and opens to
// its diff, and a call opens to its result without repeating what its row already says.

export type Call = {
  kind: "call"
  key: string
  tool: string
  input?: Record<string, unknown>
  // result is absent while the call runs, and for a call whose result was not kept.
  result?: TranscriptItem
}

export type Entry = { kind: "thought"; key: string; text: string } | Call

export type Row =
  | { kind: "message"; key: string; text: string }
  | { kind: "notice"; key: string; text: string }
  | { kind: "work"; key: string; entries: Entry[] }

// rows groups a transcript's items: consecutive thoughts and calls become one row of work, a result joins its
// call, and a message or a notice ends the group. A result whose call lies on an earlier page stands as a call
// without a name.
export function rows(items: TranscriptItem[]): Row[] {
  const out: Row[] = []
  const calls = new Map<string, Call>()
  let group: Entry[] | null = null

  const work = (entry: Entry) => {
    if (!group) {
      group = []
      out.push({ kind: "work", key: entry.key, entries: group })
    }
    group.push(entry)
  }

  for (const item of items) {
    const key = String(item.seq)

    switch (item.kind) {
      case "message":
        group = null
        out.push({ kind: "message", key, text: item.text ?? "" })
        break
      case "notice":
        group = null
        out.push({ kind: "notice", key, text: item.text ?? "" })
        break
      case "reasoning":
        work({ kind: "thought", key, text: item.text ?? "" })
        break
      case "tool_call": {
        const call: Call = { kind: "call", key, tool: item.tool ?? "", input: item.input }
        if (item.call_id) calls.set(item.call_id, call)
        work(call)
        break
      }
      case "tool_result": {
        const call = item.call_id ? calls.get(item.call_id) : undefined
        if (call) call.result = item
        else work({ kind: "call", key, tool: "", result: item })
        break
      }
    }
  }

  return out
}

// settled splits a finished transcript into its last message and all that came before or after it, which folds
// under one line: t3code hides a finished turn's entries but its terminal message
// (apps/web/src/components/chat/MessagesTimeline.logic.ts at b8c2cfad). A transcript without a message, or with
// nothing but its message, does not fold.
export function settled(list: Row[]): { before: Row[]; last?: Row } {
  const at = list.findLastIndex((r) => r.kind === "message")
  if (at < 0 || list.length === 1) return { before: list }

  return { before: list.filter((_, i) => i !== at), last: list[at] }
}

// liveTail is how many of a running worker's last steps stay out of the fold (Omnigent STREAMING_TAIL).
export const liveTail = 3

// fold splits a group of work into what folds under one line and what stays in view. A finished group folds
// whole; in a running worker's newest group the last steps stay out, so the reader watches them (Omnigent
// partitionToolRun). One step alone never folds.
export function fold(entries: Entry[], live: boolean): { folded: Entry[]; shown: Entry[] } {
  const keep = live ? liveTail : 0
  const folded = entries.slice(0, Math.max(0, entries.length - keep))

  return folded.length > 1
    ? { folded, shown: entries.slice(folded.length) }
    : { folded: [], shown: entries }
}

// summary says a folded group in words, as the CLIs print their steps: "Ran 1 shell command, read 2 files"
// (Omnigent formatToolRunLabel). A group of thoughts alone is "Thought".
export function summary(entries: Entry[]): string {
  const calls = entries.filter((e) => e.kind === "call")
  if (calls.length === 0) return "Thought"

  return formatToolRunLabel(calls.map((c) => ({ name: c.tool, args: c.input })))
}

// failed counts the calls of a group that failed.
export function failed(entries: Entry[]): number {
  return entries.filter((e) => e.kind === "call" && e.result?.failed).length
}

// title is a call's row: a verb and what it is about ("Read web/src/app.tsx"), or for a shell its command.
export function title(entry: Call): ToolTitle {
  if (!entry.tool) return { verb: "Result of an earlier call", body: "" }

  return formatToolTitle(entry.tool, entry.input ?? {})
}

export type Kind = "shell" | "list" | "read" | "edit" | "search" | "other"

// kind sorts a call by what it does, for its icon.
export function kind(entry: Call): Kind {
  return toolCallCategory({ name: entry.tool, args: entry.input })
}

// command is a shell call's command: its first line for the row, how many lines follow, and the whole of it for
// the opened row. It is undefined for other tools.
export function command(entry: Call): { first: string; more: number; whole: string } | undefined {
  const whole = shellCommand(entry.tool, entry.input ?? {})
  if (whole === null) return undefined

  const { firstLine, moreLines } = shellCommandPreview(whole)

  return { first: firstLine, more: moreLines, whole }
}

export type DiffLine = { kind: "add" | "del"; text: string }

// The most of an edit drawn (Orca native-chat-diff DEFAULT_MAX_DIFF_LINES).
const diffLines = 120

function linesOf(value: unknown): string[] {
  if (typeof value !== "string" || value === "") return []
  const lines = value.split("\n")
  if (lines.at(-1) === "") lines.pop()

  return lines
}

// diff is the change an edit call carries, as the lines it removes and the lines it adds (Orca diffFromToolCall):
// the old and new text of an Edit, each edit of a MultiEdit, the whole content of a Write. It is undefined for a
// call that carries no change. The node may have cut long text; the counts are of what was kept.
export function diff(
  entry: Call,
): { lines: DiffLine[]; added: number; removed: number; cut: boolean } | undefined {
  if (kind(entry) !== "edit" || !entry.input) return undefined

  const input = entry.input
  const edits = Array.isArray(input.edits) ? (input.edits as Record<string, unknown>[]) : [input]
  const lines: DiffLine[] = []

  for (const e of edits) {
    for (const text of linesOf(e.old_string)) lines.push({ kind: "del", text })
    for (const text of linesOf(e.new_string ?? e.content)) lines.push({ kind: "add", text })
  }

  if (lines.length === 0) return undefined

  return {
    lines: lines.slice(0, diffLines),
    added: lines.filter((l) => l.kind === "add").length,
    removed: lines.filter((l) => l.kind === "del").length,
    cut: lines.length > diffLines,
  }
}

// rest is a call's input as JSON, for the calls whose row cannot say all of it: a tool with no title of its own,
// and the worker's ask and finish, whose rows show a first line. Other calls open to their result alone (Orca
// NativeChatToolLine: "its arguments are the row's own words").
export function rest(entry: Call): string | undefined {
  if (!entry.input || Object.keys(entry.input).length === 0) return undefined

  const own = ["ask", "finish"].includes(splitToolName(entry.tool).tool)

  return own || title(entry).toolName !== undefined
    ? JSON.stringify(entry.input, null, 2)
    : undefined
}

// The most of a result shown before the reader asks for all of it (Omnigent ToolCodePanel: 80 lines or 12 000
// characters).
export const previewLines = 80
export const previewChars = 12_000

// preview is the start of a long output and how many lines were left out.
export function preview(text: string): { text: string; hidden: number } {
  const lines = text.split("\n")
  let shown = lines.slice(0, previewLines).join("\n")
  if (shown.length > previewChars) shown = shown.slice(0, previewChars)
  if (shown.length === text.length) return { text, hidden: 0 }

  return { text: shown, hidden: Math.max(1, lines.length - shown.split("\n").length) }
}
