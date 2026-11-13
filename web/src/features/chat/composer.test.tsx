import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { expect, test, vi } from "vitest"

import { Composer } from "./composer"

const setup = (working = false, send: (text: string) => Promise<void> = async () => {}) => {
  const onSend = vi.fn<(text: string, files: unknown[]) => Promise<void>>(send)
  const onStop = vi.fn<() => void>()
  render(<Composer onSend={onSend} onStop={onStop} working={working} bot="coordinator" />)

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
