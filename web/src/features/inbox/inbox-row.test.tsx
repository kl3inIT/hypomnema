import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { expect, test } from "vitest"

import type { Todo } from "@/api/generated/types.gen"
import { TooltipProvider } from "@/components/ui/tooltip"

import { InboxRow } from "./inbox-row"
import { rowsOf, type Row } from "./inbox-state"
import type { KeptChats } from "./use-origin"

const now = new Date(2026, 10, 25, 10, 0).getTime()

const todo = (id: string, m: Partial<Todo> = {}): Todo => ({
  id,
  revision: 1,
  title: `To-do ${id}`,
  notes: "",
  done_when: "",
  draft: "",
  due: { kind: "none" },
  assignee: { kind: "me" },
  state: "open",
  source: { kind: "owner" },
  created_by: { kind: "owner" },
  close_reason: "",
  evidence: [],
  stale: false,
  priority: 0,
  priority_reason: "",
  created_at: new Date(now - 3_600_000).toISOString(),
  updated_at: new Date(now - 3_600_000).toISOString(),
  ...m,
})

const bots = (id: string) => (id === "thu-ky" ? "Thư ký" : id)

function show(row: Row, chats: KeptChats = new Map()) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <ul>
          <InboxRow
            row={row}
            chats={chats}
            botName={bots}
            now={now}
            selected={false}
            unread={false}
            onOpen={() => {}}
            onAction={() => {}}
          />
        </ul>
      </TooltipProvider>
    </QueryClientProvider>,
  )

  return screen.getByTestId("inbox-row")
}

const one = (t: Todo) => rowsOf({ todos: [t] }, now)[0]!

// Scenario: issue #1140. A row's avatar is the platform it came from, named for a screen reader: the channel's own
// logo, a meeting's, the owner's pen, and the Bot's face for what came from its chat.
test.each([
  [todo("z", { source: { kind: "zalo", id: "1" } }), "zalo", "Zalo"],
  [todo("w", { source: { kind: "whatsapp", id: "1" } }), "whatsapp", "WhatsApp"],
  [todo("l", { source: { kind: "lark", account: "me" } }), "lark", "Lark"],
  [todo("g", { source: { kind: "gmail", id: "thr_1" } }), "gmail", "Gmail"],
  [todo("m", { source: { kind: "meeting", id: "mtg_1" } }), "meeting", "Meeting"],
  [todo("h"), "hand", "Added by you"],
  [
    todo("c", { source: { kind: "chat", id: "cht_1" }, created_by: { kind: "bot", id: "thu-ky" } }),
    "chat",
    "Chat",
  ],
])("a row from %#'s source shows its platform as the avatar", (t, source, name) => {
  const row = show(one(t))
  const avatar = row.querySelector(`[data-source="${source}"]`)

  expect(avatar).toHaveTextContent(name)
  expect(avatar?.querySelector("svg")).not.toBeNull()
})

// Scenario: issue #1140. Below the title, who said it and where, with no second mark of the platform and no state
// badge on the avatar, which the group's heading says.
test("a row's line names the person and the place, with one mark and no state badge", () => {
  const row = show(
    one(
      todo("z", {
        title: "Send the deck",
        source: { kind: "zalo", id: "g1", account: "me" },
        created_by: { kind: "bot", id: "thu-ky" },
      }),
    ),
    new Map([["zalo:g1", { name: "BeyondPilot Platform Discussion", label: "me" }]]),
  )

  expect(row.querySelector('[data-slot="from"]')).toHaveTextContent(
    "BeyondPilot Platform Discussion",
  )
  expect(row.querySelectorAll("[data-source]")).toHaveLength(1)
  expect(row).not.toHaveTextContent(/To-do|Proposed|Snoozed/)
})
