import { render, screen } from "@testing-library/react"
import { describe, expect, test } from "vitest"

import type { ChatMessage } from "@/api/generated/types.gen"

import { ChatItemView } from "./chat-items"
import { chatItems, chatReducer, emptyChat } from "./chat-state"

const row = (m: Partial<ChatMessage>): ChatMessage => ({
  seq: 7,
  chat_id: "cht_1",
  role: "system",
  kind: "approval",
  text: 'Approved: "Send mail: minh@example.com" (apr_1), this once.',
  created_at: "2026-11-15T09:30:00Z",
  ...m,
})

const drawn = (m: ChatMessage) => {
  const items = chatItems(chatReducer(emptyChat, { type: "message", message: m }))
  const item = items[0]
  if (!item) throw new Error("nothing drawn")

  render(<ChatItemView item={item} working={false} botName="reporter" />)

  return items
}

// An answered Approval stays in its chat as a row (issue #465): the answer, what was asked, how far it reaches.
describe("an answered Approval's row", () => {
  test("says it was approved, what was asked and for how long", () => {
    const items = drawn(
      row({
        approval_id: "apr_1",
        approval: {
          title: "Send mail: minh@example.com",
          action_name: "Send mail",
          status: "approved",
          scope: "session",
          decided_at: "2026-11-15T09:30:00Z",
        },
      }),
    )

    expect(items.map((i) => i.kind)).toEqual(["approval"])
    const group = screen.getByRole("note", { name: "Approved: Send mail: minh@example.com" })
    expect(group).toHaveTextContent("Send mail · For the rest of this chat")
    expect(group.querySelector("time")).toHaveAttribute("datetime", "2026-11-15T09:30:00Z")
  })

  test("says a denial with no scope", () => {
    drawn(
      row({
        approval_id: "apr_2",
        approval: {
          title: "Send mail: lan@example.com",
          action_name: "Send mail",
          status: "rejected",
          decided_at: "2026-11-15T09:31:00Z",
        },
      }),
    )

    const group = screen.getByRole("note", { name: "Denied: Send mail: lan@example.com" })
    expect(group).toHaveAttribute("data-approval", "rejected")
    expect(group).not.toHaveTextContent("·")
  })

  test("from before rows named their Approval is a notice", () => {
    expect(drawn(row({})).map((i) => i.kind)).toEqual(["notice"])
  })
})
