import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Browser } from "@/api/generated/types.gen"

import { BrowserTabCard } from "./browser-tab-card"
import { OpenBrowserTabContext } from "./open-tab-context"

afterEach(() => vi.restoreAllMocks())

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } })

function show(browser: Browser, open: (tab: string) => void) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async () => json(browser))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <OpenBrowserTabContext value={{ chat: "c1", open }}>
        <BrowserTabCard args={{ url: "https://maps.example/login", headed: true }} />
      </OpenBrowserTabContext>
    </QueryClientProvider>,
  )
}

const running = { installed: true, running: true, held: false, chrome: "Chrome 157" }

// Scenario: the Bot opened a page in the chat; its card names the page and opens the Bot's tab.
test("the card shows the chat's tab and opens it", async () => {
  const open = vi.fn<(tab: string) => void>()
  show(
    {
      ...running,
      tabs: [
        {
          id: "d-1",
          kind: "worker",
          chat_id: "c1",
          url: "https://w.example/",
          title: "W",
          views: 0,
        },
        {
          id: "b-c1",
          kind: "coordinator",
          chat_id: "c1",
          bot_id: "bot_1",
          url: "https://maps.example/login",
          title: "Sign in to Maps",
          views: 0,
        },
      ],
    },
    open,
  )

  const card = await screen.findByRole("region", { name: "Browser tab: Sign in to Maps" })
  expect(card).toHaveTextContent("https://maps.example/login")

  await userEvent.click(screen.getByRole("button", { name: "Open the tab" }))
  expect(open).toHaveBeenCalledExactlyOnceWith("b-c1")
})

// Scenario: the tab was closed since; the card gives the address that was opened, and nothing to open.
test("a closed tab's card has nothing to open", async () => {
  show({ ...running, tabs: [] }, vi.fn())

  await screen.findByText("The tab is closed")
  expect(
    screen.getByRole("region", { name: "Browser tab: https://maps.example/login" }),
  ).toBeVisible()
  expect(screen.queryByRole("button", { name: "Open the tab" })).toBeNull()
})
