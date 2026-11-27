import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { expect, it, vi } from "vitest"

import type { Account, GatewayKey, Provider } from "@/api/generated/types.gen"

import { KeySheet } from "./gateway-key-sheet"

const account = (id: string, runtime: string, label: string, position: number): Account => ({
  id,
  runtime,
  label,
  status: "active",
  position,
})

const providers: Provider[] = [
  { name: "claude", kind: "claude_subscription", models: [] } as unknown as Provider,
  { name: "codex", kind: "codex_subscription", models: [] } as unknown as Provider,
]

const laptop: GatewayKey = {
  id: "gwk_1",
  name: "beyondpilot",
  prefix: "hyp_gateway_v1_abcdef",
  models: ["*"],
  endpoints: ["search", "fetch", "images", "audio"],
  search_order: [],
  created_at: new Date().toISOString(),
}

function renderSheet(key: GatewayKey) {
  const sent: { method: string; url: string; body: unknown }[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })

    if (request.method === "PATCH") {
      const body = await request.clone().json()
      sent.push({ method: request.method, url: path, body })

      return json({ ...key, ...body })
    }

    switch (path) {
      case "/accounts":
        return json({
          data: [
            account("acc_1", "claude", "work", 1),
            account("acc_2", "claude", "home", 2),
            account("acc_3", "codex", "chatgpt", 1),
          ],
        })
      case "/providers":
        return json({ data: providers })
      default:
        return json({ data: [] })
    }
  })

  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={queryClient}>
      <KeySheet gatewayKey={key} />
    </QueryClientProvider>,
  )

  return sent
}

// Scenario (issue #1277): the owner edits a key in its sheet. Routing is closed, its header saying the key follows
// Settings. Opened, it limits the key to one Claude account, and gives it its own search order, Codex then Claude,
// Claude moved first after; the header says so, and saving sends both. Only these with no account chosen is refused
// before anything is sent.
it("the owner limits a key's accounts and gives it its own search order", async () => {
  const user = userEvent.setup()
  const sent = renderSheet(laptop)

  await user.click(screen.getByRole("button", { name: "Edit beyondpilot" }))
  const sheet = screen.getByRole("dialog", { name: "Edit beyondpilot" })
  expect(within(sheet).getByLabelText("Name")).toHaveValue("beyondpilot")
  expect(within(sheet).getByTestId("routing-summary")).toHaveTextContent("like Settings")
  expect(within(sheet).queryByRole("region", { name: "Accounts" })).toBeNull()

  await user.click(within(sheet).getByRole("button", { name: /Routing/ }))
  const accounts = within(sheet).getByRole("region", { name: "Accounts" })
  await user.click(within(accounts).getByRole("button", { name: "Only these" }))
  await user.click(within(sheet).getByRole("button", { name: "Save changes" }))
  expect(await within(sheet).findByText("Choose an account, or every account.")).toBeInTheDocument()
  expect(sent).toHaveLength(0)

  await user.click(await within(accounts).findByRole("button", { name: "home" }))
  expect(within(accounts).getByRole("group", { name: "Claude accounts" })).toBeInTheDocument()

  const order = within(sheet).getByRole("region", { name: "Web search order" })
  await user.click(within(order).getByRole("button", { name: "Its own" }))
  await user.click(within(order).getByRole("button", { name: "Add Codex" }))
  await user.click(within(order).getByRole("button", { name: "Add Claude" }))
  await user.click(within(order).getByRole("button", { name: "Move Claude earlier" }))
  expect(within(order).getByRole("list", { name: "Asked first" })).toHaveTextContent(
    /1.*Claude.*2.*Codex/,
  )
  expect(within(sheet).getByTestId("routing-summary")).toHaveTextContent(
    "1 account · own search order",
  )

  await user.click(within(sheet).getByRole("button", { name: "Save changes" }))
  await waitFor(() => expect(sent).toHaveLength(1))
  expect(sent[0]!.url).toBe("/gateway/keys/gwk_1")
  expect(sent[0]!.body).toEqual({
    name: "beyondpilot",
    models: [],
    accounts: ["acc_2"],
    search_order: ["claude", "codex"],
  })
})

// Scenario (issue #1277): the owner sets a limited key back to every account and the order of Settings.
it("the owner sets a key's routing back to Settings", async () => {
  const user = userEvent.setup()
  const sent = renderSheet({
    ...laptop,
    accounts: [{ id: "acc_1", runtime: "claude" }],
    search_order: ["codex"],
  })

  await user.click(screen.getByRole("button", { name: "Edit beyondpilot" }))
  const sheet = screen.getByRole("dialog", { name: "Edit beyondpilot" })
  expect(within(sheet).getByTestId("routing-summary")).toHaveTextContent(
    "1 account · own search order",
  )
  await user.click(within(sheet).getByRole("button", { name: /Routing/ }))
  await user.click(
    within(within(sheet).getByRole("region", { name: "Accounts" })).getByRole("button", {
      name: "Every account",
    }),
  )
  await user.click(
    within(within(sheet).getByRole("region", { name: "Web search order" })).getByRole("button", {
      name: "Like Settings",
    }),
  )
  await user.click(within(sheet).getByRole("button", { name: "Save changes" }))

  await waitFor(() => expect(sent).toHaveLength(1))
  expect(sent[0]!.body).toMatchObject({ accounts: ["*"], search_order: [] })
})

// Scenario (review of PR #1298): every account a key was limited to was removed, so it uses none. The owner renames
// it: the name is saved, and the key keeps its limit, untouched.
it("the owner renames a key whose accounts are all gone", async () => {
  const user = userEvent.setup()
  const sent = renderSheet({ ...laptop, accounts: [] })

  await user.click(screen.getByRole("button", { name: "Edit beyondpilot" }))
  const sheet = screen.getByRole("dialog", { name: "Edit beyondpilot" })
  expect(within(sheet).getByTestId("routing-summary")).toHaveTextContent("0 accounts")

  await user.clear(within(sheet).getByLabelText("Name"))
  await user.type(within(sheet).getByLabelText("Name"), "beyondpilot-old")
  await user.click(within(sheet).getByRole("button", { name: "Save changes" }))

  await waitFor(() => expect(sent).toHaveLength(1))
  expect(sent[0]!.body).toEqual({ name: "beyondpilot-old", models: [] })
})
