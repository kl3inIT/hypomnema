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
function server(titlesReady?: Promise<void>) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")
    if (path === `/meetings/${meeting}`) {
      await titlesReady

      return json({
        meeting: { id: meeting, title: "Họp với cô Laura về Cốc Cốc", speakers: [] },
        lines: [],
      })
    }
    if (path === `/chats/${chat}`) {
      await titlesReady

      return json({ id: chat, title: "Inbox: báo cáo", bot: "bot_main" })
    }
    if (path === "/integrations") return json({ data: integrations })
    if (path === `/inbox/${other}`)
      return json({ ...todo({ id: other, title: "Backend push service" }), events: [] })
    const messages = /^\/integrations\/(zalo|whatsapp)\/connections\/main\/messages$/.exec(path)
    if (messages) {
      const query = new URL((input as Request).url).searchParams
      const kept = said.filter((m) => m.service === messages[1])
      const id = query.get("id")
      if (id) return json({ data: kept.filter((m) => m.id === id) })
      const before = Date.parse(query.get("before") ?? "")
      const chat = kept.filter((m) => m.chat === query.get("chat") && Date.parse(m.at) < before)

      return json({ data: chat.toSorted((a, b) => Date.parse(b.at) - Date.parse(a.at)) })
    }

    return json({ ...todo(), events: [] })
  })
}

// The owner's Zalo and WhatsApp accounts, linked as devices, and what the server keeps of their chats.
const other = "tdo_cwuc462tii6jsgujytmg3pzrea"
const integrations = ["zalo", "whatsapp"].map((id) => ({
  id,
  name: id,
  kind: "device",
  services: [],
  connections: [
    { integration: id, label: "main", account: id, services: [], sign_in_required: false },
  ],
}))
const message = (service: string, id: string, at: string, sender: string, text: string) => ({
  service,
  id,
  chat: `${service}-chat`,
  chat_name: service === "zalo" ? "tam hợp 96-00-04" : "Blaze <> Tasco | TTS",
  sender: sender,
  sender_name: sender,
  at,
  from_me: false,
  text,
  edited: false,
  revoked: false,
  media_kept: false,
})
const said = [
  message("zalo", "8527929591179", "2026-11-25T09:25:00Z", "Vương Phúc Thành", "chỉ push cá nhân"),
  message("zalo", "8527934507495", "2026-11-25T09:27:00Z", "Trần Viết Thanh", "vâng"),
  message("zalo", "8527940000000", "2026-11-25T09:28:00Z", "Vương Phúc Thành", "ok em"),
  message(
    "whatsapp",
    "3EB0E056F0533EEF55A863",
    "2026-11-26T07:38:00Z",
    "Trung Huynh",
    "Ok thank em nhé!",
  ),
]

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

// notesOf is the item's description, once it shows.
function notesOf() {
  return waitFor(() => {
    const notes = document.querySelector<HTMLElement>("[data-slot=notes]")
    expect(notes).not.toBeNull()

    return notes!
  })
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
  let releaseTitles = () => {}
  const titlesReady = new Promise<void>((resolve) => {
    releaseTitles = resolve
  })
  server(titlesReady)
  show(
    todo({
      notes: `## Meeting\n25/11 (họp cô Laura, ${meeting}): Tasco mua Cốc Cốc.\nItem's chat: ${chat}\n\n## Plan\n1. Tasco\n2. Cốc Cốc\n\n\`${meeting}\``,
    }),
  )

  // First wait for the Markdown to render. Then release the title responses and wait for the same links to update.
  const link = await screen.findByRole("link", { name: meeting })
  expect(link).toHaveAttribute("href", `/meetings/${meeting}`)
  expect(link).toHaveAttribute("title", meeting)
  const chatLink = screen.getByRole("link", { name: chat })
  expect(chatLink).toHaveAttribute("href", `/chats/${chat}`)
  releaseTitles()
  await waitFor(() => {
    expect(link).toHaveAccessibleName("Họp với cô Laura về Cốc Cốc")
    expect(chatLink).toHaveAccessibleName("Inbox: báo cáo")
  })

  const notes = document.querySelector("[data-slot=notes]") as HTMLElement
  expect(within(notes).getByRole("heading", { name: "Plan" })).toBeVisible()
  expect(
    within(notes)
      .getAllByRole("listitem")
      .map((li) => li.textContent),
  ).toEqual(["Tasco", "Cốc Cốc"])
  // Each line of the description is its own paragraph (issue #1186).
  expect(notes.querySelector("br")).toBeNull()
  await waitFor(() => expect(within(notes).getByText(meeting, { selector: "code" })).toBeVisible())
})

// Scenario: issue #1186, plan row 1. A description written line by line reads as paragraphs with space between
// them, as Linear's editor makes Enter a new paragraph, without a blank line between them.
test("each line of a description is a paragraph of its own", async () => {
  server()
  show(todo({ notes: "Anh Trung hỏi lại về TTS.\nCô Laura: em cứ test trước nhé." }))

  const notes = await notesOf()
  expect([...notes.querySelectorAll("p")].map((p) => p.textContent)).toEqual([
    "Anh Trung hỏi lại về TTS.",
    "Cô Laura: em cứ test trước nhé.",
  ])
})

// Scenario: issue #1186, plan row 2. A Zalo item's description names its messages, a WhatsApp message, a Lark chat
// and another item by their ids: each id the server keeps a message of is a link named "message" that opens it in
// its chat, the Lark chat opens in Lark, the item reads as its title; a number no message has stays as written.
test("a description's message ids are links that open the message in its chat", async () => {
  server()
  show(
    todo({
      source: {
        kind: "zalo",
        id: "zalo-chat",
        account: "main",
        ref: { kind: "zalo_message", ref: "8527934507495" },
      },
      notes: [
        'Thanh: "vâng" (8527934507495), chỉ push cá nhân (8527929591179).',
        'Anh Trung: "Ok thank em nhé!" (3EB0E056F0533EEF55A863).',
        "Lark nhóm (oc_9143bfddcd80cdb37d137257859e50eb), ThanhTV (om_x100b7f5fdbe13ca8edad8575cbee3ae), cho " +
          other +
          ".",
        "Hotline 0912345678 (9999999999999).",
      ].join("\n"),
    }),
  )

  const notes = await notesOf()
  await waitFor(() =>
    expect(within(notes).getAllByRole("button", { name: /^Message from / })).toHaveLength(3),
  )
  expect(notes).not.toHaveTextContent("8527934507495")
  expect(notes).not.toHaveTextContent("3EB0E056F0533EEF55A863")
  // A number that is no kept message stays text.
  expect(notes).toHaveTextContent("Hotline 0912345678 (9999999999999).")
  // A Lark message's id in an item read elsewhere names no chat the Inbox knows.
  expect(notes).toHaveTextContent("ThanhTV (om_x100b7f5fdbe13ca8edad8575cbee3ae)")

  const lark = within(notes).getByRole("link", { name: "Lark chat" })
  expect(lark).toHaveAttribute(
    "href",
    "https://applink.larksuite.com/client/chat/open?openChatId=oc_9143bfddcd80cdb37d137257859e50eb",
  )
  const item = await within(notes).findByRole("link", { name: "Backend push service" })
  expect(item).toHaveAttribute("href", `/inbox?item=todo%3A${other}`)

  // The first message opens in its chat: the messages up to it, it marked, the later one not shown.
  await userEvent.click(within(notes).getAllByRole("button", { name: /^Message from / })[0]!)
  const chat = await screen.findByRole("list", { name: "tam hợp 96-00-04" })
  await waitFor(() =>
    expect(
      within(chat)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual([expect.stringContaining("chỉ push cá nhân"), expect.stringContaining("vâng")]),
  )
  expect(within(chat).getByText("vâng").closest("li")).toHaveAttribute("aria-current", "true")
})

// Scenario: issue #1186, plan row 2. A Lark item's message id opens the Lark chat it was read in, in Lark.
test("a Lark message's id opens the Lark chat the item was read in", async () => {
  server()
  const chat = "oc_ca0b58a93ca636edc4a7367ceab0a4d1"
  show(
    todo({
      source: { kind: "lark", id: chat, account: "me" },
      notes: "PhungDM nhắn @all (om_x100b7f4e61e0c8b0eda0e1c5384cd15).",
    }),
  )

  const link = await screen.findByRole("link", { name: "message" })
  expect(link).toHaveAttribute(
    "href",
    `https://applink.larksuite.com/client/chat/open?openChatId=${chat}`,
  )
  expect(link).toHaveAttribute("title", "om_x100b7f4e61e0c8b0eda0e1c5384cd15")
})
