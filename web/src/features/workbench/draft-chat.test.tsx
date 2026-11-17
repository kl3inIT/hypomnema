import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { Bot } from "@/api/generated/types.gen"

import { DraftChat } from "./draft-chat"

beforeEach(() => window.localStorage.clear())
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } })

// The main Bot, whose id is not its name (issue #466).
const main: Bot = {
  id: "bot_mainaaaaaaaaaaaaaaaaaaaaaa",
  name: "Coordinator",
  main: true,
  position: 0,
  pinned: false,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  tools: [],
  tools_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  scopes: {},
  tool_display: "on",
  guaranteed_dispatches: 4,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
}

// Scenario: a new chat in a pane's tab with the main Bot. The message box names the Bot by its name, never by
// its id.
test("the message box names the Bot, not its id", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")
    return json({ data: path === "/bots" ? [main] : [] })
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <DraftChat id="tab_1" bot="" onStarted={() => {}} />
    </QueryClientProvider>,
  )

  const box = screen.getByRole("textbox", { name: "Message" })
  await vi.waitFor(() =>
    expect(box).toHaveAttribute("placeholder", "Ask Coordinator to build something"),
  )
  expect(box.getAttribute("placeholder")).not.toContain(main.id)
})
