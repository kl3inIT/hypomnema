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

import type { InboxChat, InboxChatSource } from "@/api/generated/types.gen"
import { SidebarProvider } from "@/components/ui/sidebar"

import { ChatsReadScreen } from "./chats-read-screen"
import { defaultsLine, why } from "./chats-read-state"

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

// The owner's accounts and chats, every name and id made up.
const sources: InboxChatSource[] = [
  { integration: "zalo", label: "main", account: "Đạt" },
  { integration: "lark", label: "me", account: "" },
]

const zalo: InboxChat[] = [
  { id: "1001", name: "Lan Anh", kind: "direct", reads: true, set_by: "default" },
  { id: "2002", name: "Team Alpha", kind: "group", reads: true, set_by: "default" },
  { id: "3003", name: "Zalo Pay", kind: "bot", reads: false, set_by: "default" },
]

const countOf = (chats: InboxChat[]) => ({
  all: chats.length,
  read: chats.filter((c) => c.reads).length,
  skipped: chats.filter((c) => !c.reads).length,
  direct: chats.filter((c) => c.kind === "direct").length,
  group: chats.filter((c) => c.kind === "group").length,
  bot: chats.filter((c) => c.kind === "bot").length,
})

// server answers as the control plane: the sources with their counts, an account's chats searched and filtered,
// and a switch, which it keeps.
function server() {
  let chats = zalo.map((c) => ({ ...c }))
  const asked: string[] = []
  const set: { path: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const url = new URL(request.url)
    const path = url.pathname.replace("/api/v1", "")

    if (path === "/inbox/chat-sources") {
      return json({
        data: sources.map((s) => ({
          ...s,
          counts: countOf(s.integration === "zalo" ? chats : []),
        })),
        defaults: { direct: true, group: true, bot: false, bot_patterns: ["\\bOA\\b"] },
      })
    }
    if (path === "/inbox/chat-sources/zalo/main" && request.method === "GET") {
      asked.push(url.search)
      const q = (url.searchParams.get("q") ?? "").toLowerCase()
      const kind = url.searchParams.get("kind")
      const state = url.searchParams.get("state")
      const searched = chats.filter((c) => c.name.toLowerCase().includes(q) || c.id.includes(q))
      const kept = searched.filter(
        (c) => (!kind || c.kind === kind) && (!state || (state === "read") === c.reads),
      )
      return json({
        data: kept,
        counts: countOf(chats),
        ...(q ? { search_counts: countOf(searched) } : {}),
        matched: kept.length,
      })
    }
    if (path === "/inbox/chat-sources/lark/me") {
      return json({ data: [], counts: countOf([]), matched: 0 })
    }
    if (request.method === "PUT") {
      const body = (await request.clone().json()) as { reads: boolean }
      set.push({ path, body })
      const id = decodeURIComponent(path.split("/").at(-1) ?? "")
      chats = chats.map((c) =>
        c.id === id
          ? { ...c, reads: body.reads, set_by: "owner", set_at: "2026-11-27T10:00:00Z" }
          : c,
      )
      return json({ integration: "zalo", label: "main", chat: id, reads: body.reads, set_at: "" })
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return { asked, set }
}

async function open() {
  const router = createRouter({
    routeTree: createRootRoute({ component: ChatsReadScreen }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <SidebarProvider>
        <RouterProvider router={router} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  await screen.findByRole("heading", { name: "Chats the secretary reads" })
}

const row = async (name: string) => (await screen.findByText(name)).closest("li") as HTMLElement

// Scenario: issue #1270. The owner opens the chats of his Zalo: a tab per account with its count, the default for
// new chats, each chat with why it is in its state. He switches Lan Anh off: the server is told, and the row then
// says it is skipped, set by him.
test("the owner switches a chat off and it says he did", async () => {
  const calls = server()
  await open()

  expect(await screen.findByRole("tab", { name: /Zalo\s*3/ })).toBeInTheDocument()
  expect(screen.getByRole("tab", { name: /Lark/ })).toBeInTheDocument()
  expect(screen.getByTestId("chat-defaults")).toHaveTextContent(
    "New chats: direct chats and groups are read; bots and official accounts are skipped.",
  )

  const lan = await row("Lan Anh")
  expect(lan).toHaveTextContent("Direct · Read · Default")
  expect(within(await row("Zalo Pay")).getByRole("switch")).not.toBeChecked()

  await userEvent.click(within(lan).getByRole("switch", { name: "The secretary reads Lan Anh" }))

  await waitFor(() =>
    expect(calls.set).toEqual([
      { path: "/inbox/chat-sources/zalo/main/1001", body: { reads: false } },
    ]),
  )
  await waitFor(async () => expect(await row("Lan Anh")).toHaveTextContent(/Skipped · Set by you/))
  expect(within(await row("Lan Anh")).getByRole("switch")).not.toBeChecked()
})

// Scenario: issue #1270. He finds a chat by a part of its name and narrows the list by kind and state; the server
// searches and filters.
test("the search and the filters ask the server", async () => {
  const calls = server()
  await open()
  await row("Lan Anh")

  await userEvent.type(screen.getByRole("searchbox", { name: "Search the chats of Zalo" }), "team")
  await waitFor(() => expect(screen.queryByText("Lan Anh")).not.toBeInTheDocument())
  expect(screen.getByText("Team Alpha")).toBeInTheDocument()
  expect(calls.asked).toContain("?q=team")

  const kinds = within(screen.getByRole("group", { name: "Kind" }))
  const states = within(screen.getByRole("group", { name: "State" }))
  expect(kinds.getAllByRole("button").map((button) => button.textContent)).toEqual([
    "All1",
    "Direct0",
    "Group1",
    "Bot0",
  ])
  expect(states.getAllByRole("button").map((button) => button.textContent)).toEqual([
    "All1",
    "Read1",
    "Skipped0",
  ])
  expect(screen.getByRole("tab", { name: /Zalo\s*3/ })).toBeInTheDocument()

  await userEvent.click(states.getByRole("button", { name: /Skipped/ }))
  expect(await screen.findByText("No chat matches.")).toBeInTheDocument()
  expect(states.getByRole("button", { name: /Read\s*1/ })).toBeInTheDocument()

  await userEvent.clear(screen.getByRole("searchbox", { name: "Search the chats of Zalo" }))
  expect(await screen.findByText("Zalo Pay")).toBeInTheDocument()
  expect(states.getByRole("button", { name: /All\s*3/ })).toBeInTheDocument()
  expect(calls.asked).toContain("?state=skipped")

  await userEvent.click(
    within(screen.getByRole("group", { name: "Kind" })).getByRole("button", { name: /Bot/ }),
  )
  await waitFor(() => expect(calls.asked).toContain("?kind=bot&state=skipped"))
})

test("why a chat is in its state, and the defaults, are said in words", () => {
  expect(why({ id: "1", name: "x", kind: "bot", reads: false, set_by: "default" })).toBe(
    "Default for bots",
  )
  expect(why({ id: "1", name: "x", kind: "group", reads: true, set_by: "default" })).toBe("Default")
  expect(
    why({
      id: "1",
      name: "x",
      kind: "direct",
      reads: false,
      set_by: "owner",
      set_at: "2026-11-27T10:00:00Z",
    }),
  ).toMatch(/^Set by you, /)
  expect(defaultsLine({ direct: true, group: false, bot: false })).toBe(
    "New chats: direct chats are read; groups and bots and official accounts are skipped.",
  )
})
