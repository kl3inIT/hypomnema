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

import type { TodoEvent } from "@/api/generated/types.gen"

import { CommentTimeline } from "./inbox-comments"

const readWorkspaceRaw = vi.hoisted(() => vi.fn<(options: unknown) => Promise<unknown>>())
vi.mock("@/api/generated/sdk.gen", async (actual) => ({
  ...(await actual<object>()),
  readWorkspaceRaw,
}))

beforeEach(() => {
  vi.stubGlobal(
    "URL",
    Object.assign(URL, { createObjectURL: () => "blob:x", revokeObjectURL: () => {} }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  readWorkspaceRaw.mockReset()
})

const chat = "cht_eag7jejewob7v65py6ydqip4wu"
const chart = "art_uhblefbe4k6v3pvvpvqml7gijq"
const gone = "art_cwuc462tii6jsgujytmg3pzrea"

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

// server keeps one Artifact, the chart a Bot showed; any other is not there.
function server() {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")
    if (path === `/artifacts/${chart}`) {
      return json({
        id: chart,
        chat_id: chat,
        bot: "bot_main",
        revision: 2,
        title: "Doanh số tháng 10",
        type: "file",
        entry: "sales.csv",
        size: 120,
        created_at: "2026-11-27T02:00:00Z",
        updated_at: "2026-11-27T03:00:00Z",
        revisions: [],
      })
    }

    return json({ code: "not_found", message: `artifact ${gone} is not there` }, 404)
  })
}

// comment is a Bot's comment on the item, with what it carries in its event.
const comment = (after: Record<string, unknown>): TodoEvent => ({
  id: 7,
  at: "2026-11-27T03:00:00Z",
  revision: 5,
  actor: { kind: "bot", id: "bot_main" },
  kind: "commented",
  before: {},
  after,
})

function show(events: TodoEvent[]) {
  const root = createRootRoute({
    component: () => (
      <CommentTimeline
        events={events}
        loading={false}
        now={Date.parse("2026-11-27T04:00:00Z")}
        botName={() => "Hypo"}
      />
    ),
  })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/inbox"] }),
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

// Scenario: issue #1207, plan rows 1 and 2. A Bot posts its export on the item: the comment shows a chip, the chat's
// own, that downloads the file from the Bot's folder through the chat the comment names.
test("a Bot's file on a comment is a chip that downloads it", async () => {
  server()
  readWorkspaceRaw.mockResolvedValueOnce({ data: new Blob(["xlsx"]) })
  show([comment({ text: "Xong: file xuất.", files: ["exports/sales.xlsx"], chat })])

  const chip = await screen.findByRole("link", { name: "Download sales.xlsx" })
  expect(chip).toHaveAttribute(
    "href",
    expect.stringMatching(
      new RegExp(`/api/v1/chats/${chat}/workspaces/bot/raw\\?path=exports%2Fsales\\.xlsx$`),
    ),
  )

  await userEvent.click(chip)

  await waitFor(() =>
    expect(readWorkspaceRaw).toHaveBeenCalledWith(
      expect.objectContaining({
        path: { chat_id: chat, workspace_id: "bot" },
        query: { path: "exports/sales.xlsx" },
      }),
    ),
  )
})

// Scenario: issue #1207, tests. A file no longer in the Bot's folder turns its chip red with the server's message.
test("a missing file says the server's message on its chip", async () => {
  server()
  readWorkspaceRaw.mockRejectedValueOnce({
    code: "not_found",
    message: "exports/sales.xlsx is not there.",
  })
  show([comment({ text: "Xong.", files: ["exports/sales.xlsx"], chat })])

  await userEvent.click(await screen.findByRole("link", { name: "Download sales.xlsx" }))

  expect(await screen.findByText("exports/sales.xlsx is not there.")).toBeInTheDocument()
})

// Scenario: issue #1207, plan rows 2 and 3. A comment carrying an Artifact shows the chat's card of it, and an
// Artifact's id in its text is a link to the Artifact's page, named by its title; one the server does not have says
// so.
test("a comment's Artifact is its card and its id in text is a link with its title", async () => {
  server()
  show([comment({ text: `Biểu đồ (${chart}) đã cập nhật.`, artifacts: [chart, gone] })])

  const artifacts = within(await screen.findByRole("list", { name: "Artifacts" }))
  expect(await artifacts.findByRole("button", { name: "Open Doanh số tháng 10" })).toBeVisible()
  expect(await artifacts.findByText(/An Artifact could not be opened: artifact/)).toBeVisible()

  const link = await screen.findByRole("link", { name: "Doanh số tháng 10" })
  expect(link).toHaveAttribute("href", `/artifacts/${chart}`)
  expect(link).toHaveAttribute("title", chart)
})

// A comment without files or Artifacts draws neither list.
test("a plain comment carries nothing", async () => {
  server()
  show([comment({ text: "Đang làm." })])

  expect(await screen.findByText("Đang làm.")).toBeVisible()
  expect(screen.queryByRole("list", { name: "Files" })).not.toBeInTheDocument()
  expect(screen.queryByRole("list", { name: "Artifacts" })).not.toBeInTheDocument()
})
