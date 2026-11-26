import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { Todo } from "@/api/generated/types.gen"
import { TooltipProvider } from "@/components/ui/tooltip"

import { InboxDetail } from "./inbox-detail"
import { rowsOf, type Action } from "./inbox-state"

beforeEach(() => {
  // The status picker's list (cmdk) watches sizes and scrolls to its item, which jsdom does not do.
  Element.prototype.scrollIntoView = () => {}
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const now = new Date(2026, 10, 25, 10, 0).getTime()
const meeting = "mtg_v44xjlbxtmqeyig3jp3drxsany"
const chat = "cht_eag7jejewob7v65py6ydqip4wu"

const todo = (m: Partial<Todo> = {}): Todo => ({
  id: "tdo_1",
  revision: 3,
  title: "Báo cáo hệ sinh thái Tasco cho cô Laura",
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
  created_at: new Date(now - 86_400_000).toISOString(),
  updated_at: new Date(now - 3_600_000).toISOString(),
  ...m,
})

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })

// server answers the reads the detail makes: the meeting and the chat an item's text names, by their ids.
function server() {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")
    if (path === `/meetings/${meeting}`) {
      return json({
        meeting: { id: meeting, title: "Họp với cô Laura về Cốc Cốc", speakers: [] },
        lines: [],
      })
    }
    if (path === `/chats/${chat}`)
      return json({ id: chat, title: "Inbox: báo cáo", bot: "bot_main" })
    if (path === "/integrations") return json([])

    return json({ ...todo(), events: [] })
  })
}

function show(item: Todo, onAction: (action: Action) => void = () => {}) {
  const [row] = rowsOf({ todos: [item] }, now)
  const root = createRootRoute({
    component: () => (
      <TooltipProvider>
        <InboxDetail
          row={row!}
          now={now}
          position={null}
          phone={false}
          pending={false}
          botName={(id) => (id === "bot_main" ? "Hypo" : id)}
          mainBot="bot_main"
          chats={new Map()}
          onBack={() => {}}
          onStep={() => {}}
          onAction={onAction}
          onAnswered={() => {}}
          onSaveDraft={() => {}}
        />
      </TooltipProvider>
    ),
  })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/inbox"] }),
  })

  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

// field is the value of one of the detail's property rows, by its label.
async function field(label: string) {
  const term = await screen.findByText(label, { selector: "dt" })

  return term.nextElementSibling as HTMLElement
}

// Scenario: issue #1173, plan row 2. An item due 23/11 read on 25/11 says it is late, and by how much, in the late
// colour, not only "Due 23/11".
test("a late item's Date says Late and how late", async () => {
  server()
  show(todo({ due: { kind: "date", date: "2026-11-23", zone: "Asia/Ho_Chi_Minh" } }))

  const date = await field("Date")
  expect(date).toHaveTextContent("Due 23/11")
  expect(date).toHaveTextContent("Late · 2 days")
  expect(within(date).getByText("Due 23/11", { exact: false }).closest("[data-tone]")).toHaveClass(
    "text-failed",
  )
})

// Scenario: issue #1173, plan row 1. The detail's properties are circle's: the status with its name, which moves
// the item as the top bar's buttons do; who does it with their avatar; the date with a calendar.
test("the detail shows the status by name, the avatar and a calendar", async () => {
  server()
  const onAction = vi.fn<(action: Action) => void>()
  show(
    todo({
      assignee: { kind: "bot", name: "bot_main" },
      due: { kind: "date", date: "2026-11-27", zone: "Asia/Ho_Chi_Minh" },
    }),
    onAction,
  )

  const status = within(await field("Status")).getByRole("combobox", { name: "Status: Open" })
  expect(status).toHaveTextContent("Open")
  expect(
    (await field("Who")).querySelector("[data-slot=who-avatar][data-who=bot] svg"),
  ).not.toBeNull()
  expect(await field("Who")).toHaveTextContent("Hypo")
  expect((await field("Date")).querySelector("svg.lucide-calendar-days")).not.toBeNull()
  expect(await field("Date")).not.toHaveTextContent("Late")

  await userEvent.click(status)
  expect(await screen.findByRole("option", { name: "Dropped" })).toBeVisible()
  await userEvent.click(screen.getByRole("option", { name: "Done" }))
  expect(onAction).toHaveBeenCalledWith("done")
})

// Scenario: issue #1173, plan rows 4 and 5. A description is Markdown, and a meeting's and a chat's ids in it are
// links named by the meeting's and the chat's titles; an id in code stays as it was written.
test("a description's ids are links with the meeting's title", async () => {
  server()
  show(
    todo({
      notes: `## Meeting\n25/11 (họp cô Laura, ${meeting}): Tasco mua Cốc Cốc.\nItem's chat: ${chat}\n\n## Plan\n1. Tasco\n2. Cốc Cốc\n\n\`${meeting}\``,
    }),
  )

  const link = await screen.findByRole("link", { name: "Họp với cô Laura về Cốc Cốc" })
  expect(link).toHaveAttribute("href", `/meetings/${meeting}`)
  expect(link).toHaveAttribute("title", meeting)
  const chatLink = await screen.findByRole("link", { name: "Inbox: báo cáo" })
  expect(chatLink).toHaveAttribute("href", `/chats/${chat}`)

  const notes = document.querySelector("[data-slot=notes]") as HTMLElement
  expect(within(notes).getByRole("heading", { name: "Plan" })).toBeVisible()
  expect(
    within(notes)
      .getAllByRole("listitem")
      .map((li) => li.textContent),
  ).toEqual(["Tasco", "Cốc Cốc"])
  // Each line of the description stays its own line, as it was written.
  expect(notes.querySelector("br")).not.toBeNull()
  await waitFor(() => expect(within(notes).getByText(meeting, { selector: "code" })).toBeVisible())
})
