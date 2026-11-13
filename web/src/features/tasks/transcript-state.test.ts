import { expect, test } from "vitest"

import type { TranscriptItem } from "@/api/generated/types.gen"

import {
  command,
  diff,
  failed,
  fold,
  preview,
  previewLines,
  rest,
  rows,
  summary,
  title,
  type Call,
  type Entry,
} from "./transcript-state"

let seq = 0
const item = (m: Partial<TranscriptItem>): TranscriptItem => ({
  seq: ++seq,
  kind: "message",
  created_at: "2026-11-10T10:00:00Z",
  ...m,
})
const call = (id: string, tool: string, input: Record<string, unknown> = {}) =>
  item({ kind: "tool_call", call_id: id, tool, input })
const result = (id: string, text: string, isFailed = false) =>
  item({ kind: "tool_result", call_id: id, text, failed: isFailed })

function work(items: TranscriptItem[]): Entry[] {
  return (rows(items)[0] as { entries: Entry[] }).entries
}

function one(tool: string, input: Record<string, unknown> = {}): Call {
  return work([call("a", tool, input)])[0] as Call
}

// Scenario: a worker says what it will do, works, and says what it found. Its work between the two messages is
// one row, each result joined to its call; a notice stands on its own.
test("work between two messages is one row", () => {
  const list = rows([
    item({ text: "I will read the handler." }),
    item({ kind: "reasoning", text: "Where is it?" }),
    call("c1", "Grep", { pattern: "health" }),
    result("c1", "handlers.go:12"),
    call("c2", "Read", { file_path: "handlers.go" }),
    result("c2", "package clientapi"),
    item({ text: "Found it." }),
    item({ kind: "notice", text: "Rate limited until 11:00" }),
    call("c3", "Bash", { command: "go test ./..." }),
  ])

  expect(list.map((r) => r.kind)).toEqual(["message", "work", "message", "notice", "work"])

  const entries = (list[1] as { entries: Entry[] }).entries
  expect(entries.map((e) => e.kind)).toEqual(["thought", "call", "call"])
  expect(entries[1]).toMatchObject({ tool: "Grep", result: { text: "handlers.go:12" } })

  const running = (list[4] as { entries: Entry[] }).entries[0] as Call
  expect(running.tool).toBe("Bash")
  expect(running.result).toBeUndefined()
})

// Scenario: the page shown starts in the middle of a transcript, with the result of a call on the page before.
test("a result without its call still shows", () => {
  const [entry] = work([result("earlier", "ok")])

  expect(entry).toMatchObject({ kind: "call", tool: "", result: { text: "ok" } })
  expect(title(entry as Call).verb).toBe("Result of an earlier call")
})

// Scenario: a finished worker's work folds whole. A running worker's last three steps stay in view and only the
// steps before them fold; one step alone is never folded.
test("a running worker's last steps stay out of the fold", () => {
  const five = work(["a", "b", "c", "d", "e"].map((id) => call(id, "Read", { file_path: id })))
  const keys = (entries: Entry[]) => entries.map((e) => (e as Call).input?.file_path)

  const done = fold(five, false)
  expect([keys(done.folded), keys(done.shown)]).toEqual([["a", "b", "c", "d", "e"], []])

  const live = fold(five, true)
  expect([keys(live.folded), keys(live.shown)]).toEqual([
    ["a", "b"],
    ["c", "d", "e"],
  ])

  const four = fold(five.slice(0, 4), true)
  expect([keys(four.folded), keys(four.shown)]).toEqual([[], ["a", "b", "c", "d"]])

  expect(fold(five.slice(0, 1), false)).toEqual({ folded: [], shown: five.slice(0, 1) })
})

test.each([
  [[call("a", "Read"), call("b", "Read"), call("c", "Bash")], "Ran 1 shell command, read 2 files"],
  [[call("a", "Edit"), call("b", "Write"), call("c", "Grep")], "Edited 2 files, ran 1 search"],
  [[call("a", "Bash", { command: "ls web" })], "Listed 1 directory"],
  [[call("a", "Read"), call("b", "mcp__github__create_pr")], "Read 1 file, called 1 other tool"],
  [[call("a", "mcp__github__create_pr"), call("b", "TodoWrite")], "Called 2 tools"],
  [[item({ kind: "reasoning", text: "hm" })], "Thought"],
])("a group is said in words: %#", (items, want) => {
  expect(summary(work(items))).toBe(want)
})

test("a group counts its failed calls", () => {
  const entries = work([
    call("a", "Bash"),
    result("a", "FAIL", true),
    call("b", "Bash"),
    result("b", "ok"),
  ])

  expect(failed(entries)).toBe(1)
})

test.each([
  ["Read", { file_path: "web/src/app.tsx" }, "Read", "web/src/app.tsx"],
  ["Edit", { file_path: "web/src/app.tsx", old_string: "a" }, "Edit", "web/src/app.tsx"],
  ["Grep", { pattern: "TODO" }, "Search:", "TODO"],
  ["TodoWrite", { todos: [] }, "Update todos", ""],
  [
    "mcp__hypomnema__ask",
    { text: "Public or behind a login?\nIt matters." },
    "Ask:",
    "Public or behind a login?",
  ],
  ["mcp__hypomnema__finish", { summary: "Added the page." }, "Report:", "Added the page."],
  ["read", { path: "notes.md" }, "Read", "notes.md"],
])("%s is a verb and what it is about", (tool, input, verb, body) => {
  expect(title(one(tool, input))).toMatchObject({ verb, body })
})

// A tool no one named here still reads as an action, with its server as a tag and its short inputs.
test("an unknown MCP tool is named from its name", () => {
  const name = title(one("mcp__linear__issue_create", { title: "Add /health", team_id: 7 }))

  expect(name).toMatchObject({ verb: "Create issue", body: "Add /health · 7", server: "linear" })
  expect(name.toolName).toBe("mcp__linear__issue_create")
})

// A shell row is the command, as the CLI prints it: Claude's own description when it gave one, else the first
// line that is not a comment; Codex's login-shell wrapper is peeled off.
test("a shell call is its command", () => {
  const script = one("Bash", { command: "# build first\nmake build\nmake test" })
  expect(title(script)).toMatchObject({ verb: null, isShellCommand: true })
  expect(command(script)).toEqual({
    first: "make build",
    more: 2,
    whole: "# build first\nmake build\nmake test",
  })

  expect(title(one("Bash", { command: "ls", description: "List the files" }))).toEqual({
    verb: null,
    body: "List the files",
  })

  expect(command(one("shell", { command: "/bin/bash -lc 'go test ./...'" }))?.first).toBe(
    "go test ./...",
  )
  expect(command(one("Read", { file_path: "a" }))).toBeUndefined()
})

// An edit shows what it removed and what it added; a new file is all added; other calls carry no change.
test("an edit carries its change", () => {
  expect(
    diff(one("Edit", { file_path: "a.go", old_string: "x := 1\n", new_string: "x := 2\ny := 3" })),
  ).toEqual({
    lines: [
      { kind: "del", text: "x := 1" },
      { kind: "add", text: "x := 2" },
      { kind: "add", text: "y := 3" },
    ],
    added: 2,
    removed: 1,
    cut: false,
  })

  expect(diff(one("Write", { file_path: "new.md", content: "# Title\n" }))).toMatchObject({
    added: 1,
    removed: 0,
  })

  const many = diff(
    one("MultiEdit", {
      file_path: "a.go",
      edits: [
        { old_string: "a", new_string: "b" },
        { old_string: "c", new_string: "d" },
      ],
    }),
  )
  expect(many).toMatchObject({ added: 2, removed: 2 })

  const long = diff(one("Write", { file_path: "big", content: "line\n".repeat(500) }))
  expect(long).toMatchObject({ added: 500, cut: true })
  expect(long?.lines).toHaveLength(120)

  expect(diff(one("Read", { file_path: "a.go" }))).toBeUndefined()
})

// A call opens to its result; its input shows only where the row cannot say all of it.
test("an input shows only when its row does not say it", () => {
  expect(rest(one("Read", { file_path: "a.go", limit: 20 }))).toBeUndefined()
  expect(rest(one("Bash", { command: "ls" }))).toBeUndefined()
  expect(rest(one("mcp__linear__issue_create", { title: "Add /health" }))).toContain("Add /health")
  expect(rest(one("mcp__hypomnema__ask", { text: "a\nb" }))).toContain("a\\nb")
})

// A long output shows its start and says how many lines wait; a short one shows whole.
test("a long result is cut to a preview", () => {
  const long = Array.from({ length: 200 }, (_, i) => `line ${i}`).join("\n")

  const cut = preview(long)
  expect(cut.text.split("\n")).toHaveLength(previewLines)
  expect(cut.hidden).toBe(200 - previewLines)

  expect(preview("ok")).toEqual({ text: "ok", hidden: 0 })
  expect(preview("x".repeat(20_000)).hidden).toBe(1)
})
