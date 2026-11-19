import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { AppSidebar } from "./app-shell"

beforeEach(() => {
  window.localStorage.clear()
  document.cookie = "sidebar_state=; max-age=0; path=/"
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
  // Every list the sidebar reads is empty; the Tasks' board has its own shape.
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(input instanceof Request ? input.url : String(input), "http://h")
    const body = url.pathname.endsWith("/tasks") ? { after: 0, data: [], runs: [] } : { data: [] }
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  })
})
afterEach(() => vi.restoreAllMocks())

function open(defaultOpen = true) {
  const router = createRouter({
    routeTree: createRootRoute({ component: () => <AppSidebar /> }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <SidebarProvider defaultOpen={defaultOpen}>
        <RouterProvider router={router} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

// Scenario (issue #941): on a computer the owner hides the sidebar from its button, which leaves a strip with the
// button that brings it back; Ctrl+B does the same, and the choice is kept in the cookie for the next visit.
test("the sidebar hides and shows from its button and Ctrl+B, and remembers the choice", async () => {
  open()
  const user = userEvent.setup()

  await user.click(await screen.findByRole("button", { name: "Hide sidebar" }))
  expect(screen.getByRole("button", { name: "Show sidebar" })).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: "Hypomnema" })).not.toBeInTheDocument()
  expect(document.cookie).toContain("sidebar_state=false")

  await user.keyboard("{Control>}b{/Control}")
  expect(await screen.findByRole("button", { name: "Hide sidebar" })).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Hypomnema" })).toBeInTheDocument()
  expect(document.cookie).toContain("sidebar_state=true")
})

test("a sidebar hidden last time opens hidden", async () => {
  open(false)

  expect(await screen.findByRole("button", { name: "Show sidebar" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Hide sidebar" })).not.toBeInTheDocument()
})
