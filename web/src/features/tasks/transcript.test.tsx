import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { TranscriptItem } from "@/api/generated/types.gen"

import { Transcript } from "./transcript"

afterEach(() => vi.restoreAllMocks())

const item = (seq: number, m: Partial<TranscriptItem>): TranscriptItem => ({
  seq,
  kind: "message",
  created_at: "2026-11-10T10:00:00Z",
  ...m,
})

const kept = [
  item(1, { text: "I will run the tests." }),
  item(2, { kind: "tool_call", call_id: "c1", tool: "Bash", input: { command: "go test ./..." } }),
  item(3, { kind: "tool_result", call_id: "c1", text: "FAIL TestHealth", failed: true }),
  item(4, {
    kind: "tool_call",
    call_id: "c2",
    tool: "Edit",
    input: { file_path: "health.go", old_string: "return 404", new_string: "return 200\nlog()" },
  }),
  item(5, { kind: "tool_result", call_id: "c2", text: "edited" }),
]

// The server: the newest page when asked from the end, the page before it, and what follows a cursor, which
// waits for the test to send it.
function transcriptServer(pages: { newest: TranscriptItem[]; earlier?: TranscriptItem[] }) {
  let next: ((items: TranscriptItem[]) => void) | undefined
  const json = (data: TranscriptItem[], more = false) =>
    new Response(JSON.stringify({ data, has_more: more }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })

  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const request = input as Request
    const query = new URL(request.url).searchParams

    if (query.get("before") === String(Number.MAX_SAFE_INTEGER)) {
      return Promise.resolve(json(pages.newest, !!pages.earlier))
    }
    if (query.has("before")) return Promise.resolve(json(pages.earlier ?? []))

    return new Promise<Response>((resolve, reject) => {
      next = (items) => resolve(json(items))
      request.signal.addEventListener("abort", () => reject(new Error("aborted")))
    })
  })

  return {
    send: (items: TranscriptItem[]) =>
      vi.waitFor(() => expect(next).toBeDefined()).then(() => next?.(items)),
  }
}

// Scenario: the owner opens a running worker's Task. The worker's message reads as prose and its last steps
// stay in view: a shell step is its command, an edit names its file with the lines it added and removed. A step
// opens to what it printed, an edit to its diff.
test("a running worker's transcript shows its last steps, which open", async () => {
  const user = userEvent.setup()
  transcriptServer({ newest: kept })
  render(<Transcript dispatchId="dsp_1" live />)

  const log = await screen.findByRole("log", { name: "Transcript" })
  expect(within(log).getByText("I will run the tests.")).toBeInTheDocument()
  expect(within(log).getByText("Working")).toBeInTheDocument()

  expect(within(log).queryByText(/FAIL TestHealth/)).toBeNull()
  await user.click(within(log).getByRole("button", { name: /go test \.\/\.\.\./ }))
  expect(within(log).getByText("$ go test ./...")).toBeInTheDocument()
  expect(within(log).getByText("FAIL TestHealth")).toBeInTheDocument()

  const edit = within(log).getByRole("button", { name: /Edit health\.go/ })
  expect(edit).toHaveTextContent("+2 -1")
  await user.click(edit)
  expect(within(log).getByText("- return 404")).toBeInTheDocument()
  expect(within(log).getByText("+ return 200")).toBeInTheDocument()
  // The first test to draw a message also loads the Markdown renderer, which is slow on a busy machine.
}, 15_000)

// Scenario: the worker goes on while the owner watches: what the node sends next appears without a reload.
test("new items appear as they come", async () => {
  const server = transcriptServer({ newest: kept.slice(0, 1) })
  render(<Transcript dispatchId="dsp_1" live />)
  await screen.findByText("I will run the tests.")

  await server.send([item(2, { text: "All tests pass now." })])

  expect(await screen.findByText("All tests pass now.")).toBeInTheDocument()
})

// Scenario: a worker that failed: its transcript stays open with its work folded under a line that says it, and
// the page before is read on demand.
test("a failed worker's transcript folds its work and loads earlier pages", async () => {
  const user = userEvent.setup()
  transcriptServer({ newest: kept.slice(1), earlier: kept.slice(0, 1) })
  render(<Transcript dispatchId="dsp_1" live={false} />)

  const log = await screen.findByRole("log", { name: "Transcript" })
  expect(within(log).queryByText("Working")).toBeNull()
  expect(within(log).queryByRole("button", { name: /go test/ })).toBeNull()
  expect(within(log).getByText("1 failed")).toBeInTheDocument()

  await user.click(within(log).getByRole("button", { name: /Ran 1 shell command, edited 1 file/ }))
  expect(within(log).getByRole("button", { name: /Edit health\.go/ })).toBeInTheDocument()

  await user.click(within(log).getByRole("button", { name: "Load earlier" }))
  expect(await within(log).findByText("I will run the tests.")).toBeInTheDocument()
  expect(within(log).queryByRole("button", { name: "Load earlier" })).toBeNull()
})

// Scenario: a worker that succeeded: the owner sees how long it worked and its last message; the rest opens.
test("a finished worker's transcript folds to its last message", async () => {
  const user = userEvent.setup()
  transcriptServer({ newest: [...kept, item(6, { text: "The page answers 200 now." })] })
  render(<Transcript dispatchId="dsp_1" live={false} worked="6m" />)

  const log = await screen.findByRole("log", { name: "Transcript" })
  expect(within(log).getByText("The page answers 200 now.")).toBeInTheDocument()
  expect(within(log).queryByText("I will run the tests.")).toBeNull()

  await user.click(within(log).getByRole("button", { name: "Worked for 6m" }))
  expect(within(log).getByText("I will run the tests.")).toBeInTheDocument()
  expect(within(log).getByRole("button", { name: /Ran 1 shell command/ })).toBeInTheDocument()
})

test("a worker that has done nothing yet says so", async () => {
  transcriptServer({ newest: [] })
  render(<Transcript dispatchId="dsp_1" live />)

  expect(
    await screen.findByText("The worker has not said or done anything yet."),
  ).toBeInTheDocument()
})
