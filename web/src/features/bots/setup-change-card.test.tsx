import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { ChatMessage, SetupChange } from "@/api/generated/types.gen"
import { chatItems, emptyChat } from "@/features/chat/chat-state"

import { SetupChangeCard } from "./setup-change-card"
import { useSetupChanges } from "./setup-changes"

afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const change = (m: Partial<SetupChange> = {}): SetupChange => ({
  id: "chg_1",
  chat_id: "cht_1",
  kind: "bot",
  name: "researcher",
  action: "made",
  can_undo: true,
  created_at: "2026-11-13T09:01:00Z",
  ...m,
})

// Cards drawn as the chat draws them: from the changes the server lists for the chat.
function Cards() {
  const changes = useSetupChanges("cht_1")

  return (changes.data ?? []).map((c) => <SetupChangeCard key={c.id} change={c} />)
}

function show() {
  const root = createRootRoute({ component: Cards })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

// Scenario: the Bot coordinator made a Bot in this chat (issue #169). Its card says so with Open and Undo; the
// owner undoes it, and the card then says it was undone, without Undo. A change that was used since says why it
// cannot be undone and offers no Undo.
test("a setup change's card undoes it, and says why when it cannot", async () => {
  let listed = [
    change(),
    change({
      id: "chg_2",
      kind: "profile",
      name: "checker",
      action: "changed",
      can_undo: false,
      problem: "profile checker was changed or used since; change it by hand",
    }),
  ]
  const asked: string[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const url = new URL(request.url)
    asked.push(`${request.method} ${url.pathname.replace("/api/v1", "")}${url.search}`)

    if (request.method === "GET" && url.pathname.endsWith("/setup-changes"))
      return json({ data: listed })
    if (request.method === "POST" && url.pathname.endsWith("/setup-changes/chg_1/undo")) {
      listed = [{ ...listed[0]!, can_undo: false, undone_at: "2026-11-13T09:05:00Z" }, listed[1]!]

      return json(listed[0])
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })
  show()

  const made = await screen.findByRole("region", { name: "Made Bot researcher" })
  expect(made).toHaveTextContent("Applied. You can undo it.")
  expect(within(made).getByRole("link", { name: "Open" })).toHaveAttribute(
    "href",
    "/bots/researcher",
  )

  const used = screen.getByRole("region", { name: "Changed profile checker" })
  expect(used).toHaveTextContent("Can't be undone: profile checker was changed or used since")
  expect(within(used).queryByRole("button", { name: "Undo" })).toBeNull()

  await userEvent.click(within(made).getByRole("button", { name: "Undo" }))

  expect(await within(made).findByText("Undone.")).toBeVisible()
  expect(within(made).queryByRole("button", { name: "Undo" })).toBeNull()
  expect(within(made).queryByRole("link", { name: "Open" })).toBeNull()
  expect(asked).toContain("GET /setup-changes?chat_id=cht_1")
})

// The conversation puts a change's card among the messages at the moment it was made, as a Routine's.
test("a chat's items place a setup change by its time", () => {
  const message = (seq: number, at: string, m: Partial<ChatMessage> = {}): ChatMessage => ({
    seq,
    chat_id: "cht_1",
    role: "owner",
    text: `message ${seq}`,
    created_at: at,
    ...m,
  })
  const view = {
    ...emptyChat,
    messages: [
      message(1, "2026-11-13T09:00:00Z"),
      message(2, "2026-11-13T09:02:00Z", { role: "coordinator", text: "I made researcher." }),
    ],
  }

  const items = chatItems(view, {}, [{ id: "chg_1", at: "2026-11-13T09:01:00Z", change: change() }])
  expect(items.map((i) => i.kind)).toEqual(["owner", "setup-change", "reply"])
})
