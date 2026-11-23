import { act, fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { expect, test, vi } from "vitest"

import { useAttachments } from "./attachments"
import { Composer } from "./composer"
import { draftsKey } from "./drafts"

const setup = (working = false, send: (text: string) => Promise<void> = async () => {}) => {
  const onSend = vi.fn<(text: string, files: unknown[]) => Promise<void>>(send)
  const onStop = vi.fn<() => void>()
  render(<Composer onSend={onSend} onStop={onStop} working={working} botName="coordinator" />)

  return { onSend, onStop, box: screen.getByRole("textbox", { name: "Message" }) }
}

// Scenario: the owner types a message; Enter sends it and clears the box, Shift+Enter starts a new line.
test("Enter sends and Shift+Enter breaks the line", async () => {
  const user = userEvent.setup()
  const { onSend, box } = setup()

  await user.type(box, "add a page{Shift>}{Enter}{/Shift}with a test")
  expect(onSend).not.toHaveBeenCalled()

  await user.type(box, "{Enter}")
  expect(onSend).toHaveBeenCalledWith("add a page\nwith a test", [])
  expect(box).toHaveValue("")
})

// Scenario: typing Vietnamese with an input method, Enter confirms the composed word and must not send.
test("Enter while an input method composes does not send", () => {
  const { onSend, box } = setup()

  fireEvent.change(box, { target: { value: "thêm trang" } })
  fireEvent.keyDown(box, { key: "Enter", isComposing: true })

  expect(onSend).not.toHaveBeenCalled()
})

// Scenario: the server refuses the message. The box says why and gives the text back to send again.
test("a failed send keeps the text and says why", async () => {
  const user = userEvent.setup()
  const { box } = setup(false, async () => {
    throw { code: "unavailable", message: "the coordinator is not running" }
  })

  await user.type(box, "add a page{Enter}")

  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Not sent: the coordinator is not running",
  )
  expect(box).toHaveValue("add a page")

  await user.click(screen.getByRole("button", { name: "Dismiss" }))
  expect(screen.queryByRole("alert")).not.toBeInTheDocument()
})

// Scenario: the Bot is working; the owner can stop it, or add a message it gets as a follow-up. The box names the
// chat's Bot.
test("while pi works, Stop shows and asks it to stop", async () => {
  const user = userEvent.setup()
  const { onStop } = setup(true)

  await user.click(screen.getByRole("button", { name: "Stop" }))
  expect(onStop).toHaveBeenCalled()
  expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute(
    "placeholder",
    "Add to what coordinator is doing",
  )
})

test("an empty message cannot be sent", () => {
  setup()

  expect(screen.getByRole("button", { name: "Send" })).toBeDisabled()
})

// Scenario: the owner sends a message and reloads before its answer came. The page going stops the send, which may
// have reached the server; the page saves its drafts as it goes, and the sent text is not among them (issue #480).
// A send whose answer is lost and the page stays: the text is back a moment later, to send again.
test("a send whose answer is lost is not kept as the draft as the page goes", async () => {
  vi.useFakeTimers()
  try {
    const onSend = vi.fn<(text: string, files: unknown[]) => Promise<void>>(async () => {
      throw new TypeError("Failed to fetch")
    })
    render(
      <Composer
        onSend={onSend}
        onStop={() => {}}
        working={false}
        botName="coordinator"
        draft={{ key: "chat:cht_lost" }}
      />,
    )
    const box = screen.getByRole("textbox", { name: "Message" })

    fireEvent.change(box, { target: { value: "and a test for it" } })
    fireEvent.keyDown(box, { key: "Enter" })
    await act(async () => {})
    expect(onSend).toHaveBeenCalledWith("and a test for it", [])
    expect(box).toHaveValue("")

    window.dispatchEvent(new Event("pagehide"))
    expect(JSON.parse(localStorage.getItem(draftsKey) ?? "{}")).not.toHaveProperty("chat:cht_lost")

    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(box).toHaveValue("and a test for it")
    expect(screen.getByRole("alert")).toHaveTextContent("Not sent: Failed to fetch")
  } finally {
    vi.useRealTimers()
  }
})

// Scenario: the owner edits a queued message (issue #497): its text comes into the box, after what is typed there.
test("a queued message taken back to edit goes into the box", async () => {
  const user = userEvent.setup()
  const props = {
    onSend: vi.fn<(text: string) => Promise<void>>(),
    onStop: vi.fn<() => void>(),
    working: true,
    botName: "coordinator",
  }
  const { rerender } = render(<Composer {...props} put={{ text: "and the totals", at: 1 }} />)
  const box = screen.getByRole("textbox", { name: "Message" })

  expect(box).toHaveValue("and the totals")

  await user.clear(box)
  await user.type(box, "first")
  rerender(<Composer {...props} put={{ text: "then this", at: 2 }} />)

  expect(box).toHaveValue("first\n\nthen this")
})

// A composer whose files are a draft's, as the New chat page's are after a reload (issue #433).
function WithFiles({ onSend, draftKey }: { onSend: () => Promise<unknown>; draftKey: string }) {
  const attachments = useAttachments("cht_sent", "coordinator", { files: [report] })

  return (
    <Composer
      onSend={onSend}
      onStop={() => {}}
      working={false}
      botName="coordinator"
      attachments={attachments}
      draft={{ key: draftKey, chat: "cht_sent", bot: "coordinator" }}
    />
  )
}

const report = { name: "report.pdf", path: "attachments/cht_sent/report.pdf", size: 15 }
const stored = () => JSON.parse(localStorage.getItem(draftsKey) ?? "{}") as Record<string, unknown>

// Scenario: the owner sends a message with a file from the New chat page, and the page goes (a reload, the next
// New chat) before the send has finished: on the New chat page it ends only after the chat's page has opened. The
// message went with its file, so neither its text nor its file is the next draft (issue #1080).
test("a message being sent leaves nothing in the draft as the page goes", async () => {
  let sent: () => void = () => {}
  render(
    <WithFiles draftKey="new:sending" onSend={() => new Promise<void>((done) => (sent = done))} />,
  )
  const box = screen.getByRole("textbox", { name: "Message" })

  fireEvent.change(box, { target: { value: "summarise this report" } })
  fireEvent.keyDown(box, { key: "Enter" })
  await act(async () => {})

  window.dispatchEvent(new Event("pagehide"))
  expect(stored()).not.toHaveProperty("new:sending")

  await act(async () => sent())
  window.dispatchEvent(new Event("pagehide"))
  expect(stored()).not.toHaveProperty("new:sending")
})

// Scenario: the server refuses the message: its text and its file are the draft again, to send later.
test("a message that was not sent is the draft again", async () => {
  render(
    <WithFiles
      draftKey="new:refused"
      onSend={async () => {
        throw { code: "unavailable", message: "the coordinator is not running" }
      }}
    />,
  )
  const box = screen.getByRole("textbox", { name: "Message" })

  fireEvent.change(box, { target: { value: "summarise this report" } })
  fireEvent.keyDown(box, { key: "Enter" })
  expect(await screen.findByRole("alert")).toHaveTextContent("Not sent")

  window.dispatchEvent(new Event("pagehide"))
  expect(stored()["new:refused"]).toEqual({
    text: "summarise this report",
    files: [report],
    chat: "cht_sent",
    bot: "coordinator",
  })
})
