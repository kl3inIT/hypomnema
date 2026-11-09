import { render, screen } from "@testing-library/react"
import { expect, test } from "vitest"

import { ChatScreen } from "./chat-screen"

// Scenario: before the coordinator exists, the owner is told so and cannot send a message that goes nowhere.
test("the chat says the coordinator is not connected and does not send", () => {
  render(<ChatScreen />)

  expect(screen.getByText(/coordinator is not connected/i)).toBeInTheDocument()
  expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Send" })).toBeDisabled()
})
