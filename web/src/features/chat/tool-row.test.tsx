import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, test } from "vitest"

import type { ChatPart } from "@/api/generated/types.gen"

import { ChatItemView } from "./chat-items"
import type { Step } from "./chat-state"
import { outputPreview, toolDuration, toolPhrase } from "./tool-row"

const tool = (name: string, args: Record<string, unknown> | undefined, result: ChatPart): Step => ({
  kind: "tool",
  key: name,
  name,
  args,
  result,
})

const drawn = (steps: Step[]) =>
  render(
    <ChatItemView
      item={{ kind: "activity", key: "a1", steps, open: true }}
      working={false}
      bot="reporter"
    />,
  )

describe("a tool row's words (issue #441)", () => {
  test("say what the call did, as Codex names a finished call", () => {
    expect(toolPhrase("bash", { command: "ls -la\necho done" })).toEqual({
      verb: "Ran",
      target: "ls -la",
    })
    expect(toolPhrase("read", { path: "report.sql" })).toEqual({
      verb: "Read",
      target: "report.sql",
    })
    expect(toolPhrase("grep", { pattern: "TODO" })).toEqual({
      verb: "Searched for",
      target: "TODO",
    })
    expect(toolPhrase("hypomnema_web", { query: "lark cards" })).toEqual({
      verb: "hypomnema_web",
      target: "lark cards",
    })
  })

  test("name the tool alone when its Bot shows no arguments", () => {
    expect(toolPhrase("bash", undefined)).toEqual({ verb: null, target: "bash" })
  })

  test("give the time as Orca does", () => {
    expect(toolDuration(42)).toBe("42ms")
    expect(toolDuration(4200)).toBe("4s")
    expect(toolDuration(65_000)).toBe("1m 5s")
    expect(toolDuration(undefined)).toBeNull()
  })

  test("show the first 1,000 characters of an output until all is asked for", () => {
    const long = "x".repeat(1500)
    expect(outputPreview(long, false)).toEqual({ text: "x".repeat(1000), more: true })
    expect(outputPreview(long, true)).toEqual({ text: long, more: false })
    expect(outputPreview("short", false)).toEqual({ text: "short", more: false })
  })
})

describe("a tool row", () => {
  // Scenario: the Bot shows its calls in full. A failed bash call's row says what it ran, the time and that it
  // failed; it opens to the command and the output's first 1,000 characters, and Show all gives the rest.
  test("opens to the command and the output, with Show all for a long one", async () => {
    const user = userEvent.setup()
    const output = `${"row\n".repeat(300)}END`
    drawn([
      tool(
        "bash",
        { command: "cat report.sql" },
        { type: "tool_result", id: "t1", name: "bash", output, error: true, duration_ms: 4200 },
      ),
    ])

    const row = screen.getByRole("button", { name: /Ran cat report\.sql/ })
    expect(within(row).getByLabelText("failed")).toBeInTheDocument()
    expect(within(row).getByText("4s")).toBeInTheDocument()

    await user.click(row)
    expect(screen.getByLabelText("Input")).toHaveTextContent("cat report.sql")
    expect(screen.getByLabelText("Output")).not.toHaveTextContent("END")

    await user.click(screen.getByRole("button", { name: /Show all \(1,203 characters\)/ }))
    expect(screen.getByLabelText("Output")).toHaveTextContent("END")
    expect(screen.queryByRole("button", { name: /Show all/ })).not.toBeInTheDocument()
  })

  // Scenario: the Bot shows its calls on, so the server sent the tool's name without arguments, output or time.
  // The row names the tool and does not open.
  test("is a line that does not open when its Bot shows no detail", () => {
    drawn([tool("bash", undefined, { type: "tool_result", id: "t1", name: "bash" })])

    expect(screen.getByText("bash")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /bash/ })).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Output")).not.toBeInTheDocument()
  })
})
