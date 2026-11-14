import type { ChatPart } from "@/api/generated/types.gen"

// What a tool row says (issue #441). The phrase is a verb and its target, as Codex names a finished call
// (codex-rs/tui/src/exec_cell/render.rs, tool_call_labels: "Read", "Searched for", "Listed files in", "Ran"; Apache
// License 2.0) and Orca's tool line shows a verb, a target and the time on the right
// (src/shared/native-chat/tools/native-chat-tool-summary.ts, src/renderer/src/components/native-chat/tools/lines/
// NativeChatToolLine.tsx; MIT, Copyright (c) 2026 Lovecast Inc.). A call whose arguments the Bot's setting hides
// (on) is named by its tool alone, as Orca names a call it cannot describe.

export type ToolPhrase = { verb: string | null; target: string }

const verbs: Record<string, { verb: string; keys: string[] }> = {
  bash: { verb: "Ran", keys: ["command"] },
  read: { verb: "Read", keys: ["path", "file_path"] },
  write: { verb: "Wrote", keys: ["path", "file_path"] },
  edit: { verb: "Edited", keys: ["path", "file_path"] },
  grep: { verb: "Searched for", keys: ["pattern"] },
  find: { verb: "Searched for", keys: ["pattern"] },
  ls: { verb: "Listed files in", keys: ["path"] },
}

// toolPhrase is the row's words: the verb and its target from the arguments, or the tool's name alone.
export function toolPhrase(name: string, args?: Record<string, unknown>): ToolPhrase {
  const known = verbs[name]
  for (const k of known?.keys ?? ["command", "path", "file_path", "pattern", "query", "url"]) {
    const v = args?.[k]
    if (typeof v === "string" && v.trim() !== "") {
      return { verb: known?.verb ?? name, target: (v.split("\n")[0] ?? "").trim() }
    }
  }

  if (name === "ls" && args) return { verb: "Listed files in", target: "." }

  return { verb: null, target: name }
}

// toolDuration is a call's time as Orca's formatToolDuration writes it (under a second in ms, then s, m and h),
// or null when the call has none.
export function toolDuration(ms?: number): string | null {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return null
  if (ms < 1000) return `${Math.round(ms)}ms`

  const total = Math.floor(ms / 1000)
  if (total < 60) return `${total}s`

  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m ${total % 60}s`

  return `${Math.floor(minutes / 60)}h ${minutes % 60}m ${total % 60}s`
}

// previewLimit is how much of an output an open row shows before "Show all", OpenMausBot's preview of a step's
// output (src/components/ToolActivity.tsx; Apache License 2.0).
export const previewLimit = 1000

// outputPreview is the start of an output and whether there is more.
export function outputPreview(output: string, all: boolean): { text: string; more: boolean } {
  if (all || output.length <= previewLimit) return { text: output, more: false }

  return { text: output.slice(0, previewLimit), more: true }
}

// toolInput is what an open row shows above the output: the command for a shell call, else the arguments.
export function toolInput(name: string, args?: Record<string, unknown>): string {
  if (!args || Object.keys(args).length === 0) return ""

  const command = args.command
  if (name === "bash" && typeof command === "string") return command

  return JSON.stringify(args, null, 2)
}

// hasDetail says a finished call has something to open: arguments or output, which the Bot shows only at full.
export function hasDetail(args?: Record<string, unknown>, result?: ChatPart): boolean {
  return (
    (args !== undefined && Object.keys(args).length > 0) ||
    result?.output !== undefined ||
    result?.duration_ms !== undefined
  )
}
