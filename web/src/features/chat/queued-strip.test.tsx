import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, test, vi } from "vitest"

import type { QueuedChatMessage } from "@/api/generated/types.gen"

import { chatReducer, emptyChat } from "./chat-state"
import { QueuedStrip } from "./queued-strip"

const queued = (m: Partial<QueuedChatMessage>): QueuedChatMessage => ({
  id: 1,
  chat_id: "cht_1",
  text: "and the totals too",
  created_at: "2026-11-15T10:00:00Z",
  ...m,
})

// The messages written while the Bot works wait above the composer (issue #497), each with Edit, Send now and
// Remove, as Omnigent's QueuedMessagesStrip.
describe("the queue above the composer", () => {
  test("is the stream's last queue frame, whole", () => {
    const one = chatReducer(emptyChat, { type: "queue", items: [queued({}), queued({ id: 2 })] })
    const two = chatReducer(one, { type: "queue", items: [queued({ id: 2 })] })

    expect(two.queue.map((m) => m.id)).toEqual([2])
  })

  test("shows nothing when nothing waits", () => {
    const { container } = render(
      <QueuedStrip
        queue={[]}
        onEdit={vi.fn<(m: QueuedChatMessage) => void>()}
        onSendNow={vi.fn<(m: QueuedChatMessage) => void>()}
        onRemove={vi.fn<(m: QueuedChatMessage) => void>()}
      />,
    )

    expect(container).toBeEmptyDOMElement()
  })

  test("lists each message with its files and acts on the one pressed", async () => {
    const onEdit = vi.fn<(m: QueuedChatMessage) => void>()
    const onSendNow = vi.fn<(m: QueuedChatMessage) => void>()
    const onRemove = vi.fn<(m: QueuedChatMessage) => void>()
    const second = queued({
      id: 2,
      text: "",
      attachments: [
        { name: "a.png", path: "attachments/cht_1/a.png", size: 3, type: "image/png" },
        { name: "b.png", path: "attachments/cht_1/b.png", size: 3, type: "image/png" },
      ],
    })
    render(
      <QueuedStrip
        queue={[queued({}), second]}
        onEdit={onEdit}
        onSendNow={onSendNow}
        onRemove={onRemove}
      />,
    )

    const rows = within(screen.getByRole("list", { name: "Queued messages" })).getAllByRole(
      "listitem",
    )
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent("and the totals too")
    expect(rows[1]).toHaveTextContent("a.png+1")

    const user = userEvent.setup()
    await user.click(within(rows[1]!).getByRole("button", { name: "Send queued message now" }))
    await user.click(within(rows[0]!).getByRole("button", { name: "Edit queued message" }))
    await user.click(within(rows[0]!).getByRole("button", { name: "Remove queued message" }))

    expect(onSendNow).toHaveBeenCalledWith(second)
    expect(onEdit).toHaveBeenCalledWith(queued({}))
    expect(onRemove).toHaveBeenCalledWith(queued({}))
  })
})
