import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { MachineProvider, Node } from "@/api/generated/types.gen"

import { node, used } from "./fixtures"
import { MachinesScreen } from "./machines-screen"

beforeEach(() => {
  window.localStorage.clear()
  // The router scrolls to the top when it opens a page; jsdom has no scrolling.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

// The server: the nodes, the machine providers, and one answer per changing request ("POST /nodes",
// "DELETE /nodes/n1"); it keeps the changing requests it got.
function nodesServer(
  nodes: Node[],
  answers: Record<string, () => Response> = {},
  providers: MachineProvider[] = [],
) {
  const requests: { call: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const call = `${request.method} ${new URL(request.url).pathname.replace("/api/v1", "")}`
    if (call === "GET /nodes") return json({ data: nodes })
    if (call === "GET /machine-providers") return json({ data: providers })

    requests.push({
      call,
      body: await request
        .clone()
        .json()
        .catch(() => undefined),
    })

    return answers[call]?.() ?? json({ code: "not_found", message: "not found" }, 404)
  })

  return requests
}

async function open() {
  const router = createRouter({
    routeTree: createRootRoute({ component: MachinesScreen }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  await screen.findByRole("heading", { name: "Machines" })
}

const machine = (name: string) => screen.findByRole("article", { name })

// Scenario: three machines, one taking work, one drained by the owner, one that lost its connection. Each says
// its state, and the header counts them.
test("a machine says whether it is online, drained or offline", async () => {
  nodesServer([
    node("builder"),
    node("resting", { status: "drained" }),
    node("gone", {
      connected: false,
      last_seen_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    }),
  ])
  await open()

  expect(await machine("builder")).toHaveTextContent("Online")
  expect(await machine("resting")).toHaveTextContent("Drained")
  expect(await machine("gone")).toHaveTextContent(/Offline · seen 3h ago/)
  expect(screen.getByText("1 online · 1 drained · 1 offline")).toBeInTheDocument()
})

// Scenario: memory and disk are shares of the machine's totals; a meter warns from 65 % and is critical from
// 90 %, and a machine that is not connected shows its last measurement without a colour.
test("meters show use against the totals and change at 65 % and 90 %", async () => {
  nodesServer([
    node("calm", { stats: used(4, 64.9) }),
    node("busy", { stats: used(10.4, 90) }),
    node("gone", { connected: false, stats: used(15, 95) }),
  ])
  await open()

  const calm = within(await machine("calm"))
  expect(calm.getByRole("meter", { name: "Memory" })).toHaveTextContent("25.0%")
  expect(calm.getByRole("meter", { name: "Memory" })).toHaveAttribute("data-tone", "good")
  expect(calm.getByRole("meter", { name: "Memory" })).toHaveAttribute(
    "title",
    "4.0 GiB of 16.0 GiB",
  )
  expect(calm.getByRole("meter", { name: "Disk" })).toHaveAttribute("data-tone", "good")

  const busy = within(await machine("busy"))
  expect(busy.getByRole("meter", { name: "Memory" })).toHaveAttribute("data-tone", "warn")
  expect(busy.getByRole("meter", { name: "Disk" })).toHaveAttribute("data-tone", "crit")

  const gone = within(await machine("gone"))
  expect(gone.getByRole("meter", { name: "Disk" })).toHaveAttribute("data-tone", "off")
})

// Scenario: a node that was added but never connected has sent no measurement and no description of itself.
test("a machine that has reported nothing shows dashes", async () => {
  nodesServer([
    node("new", {
      connected: false,
      stats: undefined,
      system: undefined,
      runtimes: [],
      last_seen_at: undefined,
      active: 0,
    }),
  ])
  await open()

  const card = await machine("new")
  expect(within(card).queryByRole("meter")).not.toBeInTheDocument()
  expect(within(card).getAllByText("—")).toHaveLength(6)
  expect(card).toHaveTextContent("Offline · never seen")
  expect(card).toHaveTextContent("0 of 4")
})

// Scenario: the owner switches to the table; the same machines show as rows, and the choice is kept for the
// next visit.
test("the layout switches between cards and a table and is remembered", async () => {
  const user = userEvent.setup()
  nodesServer([node("builder")])
  await open()

  await machine("builder")
  expect(screen.queryByRole("table")).not.toBeInTheDocument()

  await user.click(screen.getByRole("button", { name: "table" }))

  const row = within(screen.getByRole("table")).getByRole("row", { name: /builder/ })
  expect(row).toHaveTextContent("1 of 4")
  expect(row).toHaveTextContent("claude, codex")
  expect(row).toHaveTextContent("0.4.0 · linux/amd64")
  expect(screen.queryByRole("article")).not.toBeInTheDocument()
  expect(window.localStorage.getItem("hypomnema.machines.layout")).toBe("table")
})

// Scenario: the owner adds a machine. The token the server gives is shown with the install line, and is gone
// once the dialog closes; it is kept nowhere in the browser.
test("adding a machine shows its token once", async () => {
  const user = userEvent.setup()
  const requests = nodesServer([node("builder")], {
    "POST /nodes": () => json({ node: node("fresh"), token: "hnt_secret" }, 201),
  })
  await open()

  await user.click(screen.getByRole("button", { name: "Add machine" }))
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Name"), "fresh")
  await user.type(within(dialog).getByLabelText(/Max Dispatches/), "2")
  await user.click(within(dialog).getByRole("button", { name: "Add" }))

  expect(await within(dialog).findByLabelText("Token")).toHaveTextContent("hnt_secret")
  expect(within(dialog).getByLabelText("Install line")).toHaveTextContent(
    "curl -fsSL http://localhost:3000/install/node.sh | sh",
  )
  expect(dialog).toHaveTextContent("not shown again")
  expect(requests).toEqual([{ call: "POST /nodes", body: { name: "fresh", max_dispatches: 2 } }])

  await user.click(within(dialog).getByRole("button", { name: "Done" }))

  expect(screen.queryByText("hnt_secret")).not.toBeInTheDocument()
  expect(JSON.stringify({ ...window.localStorage })).not.toContain("hnt_secret")
  expect(JSON.stringify({ ...window.sessionStorage })).not.toContain("hnt_secret")

  await user.click(screen.getByRole("button", { name: "Add machine" }))
  expect(await screen.findByRole("dialog")).not.toHaveTextContent("hnt_secret")
})

// Scenario: the owner drains a machine from its menu.
test("a machine is drained from its menu", async () => {
  const user = userEvent.setup()
  const requests = nodesServer([node("builder")], {
    "POST /nodes/builder/drain": () => json(node("builder", { status: "drained" })),
  })
  await open()

  await user.click(await screen.findByRole("button", { name: "Actions for builder" }))
  await user.click(await screen.findByRole("menuitem", { name: "Drain" }))

  await vi.waitFor(() => expect(requests.map((r) => r.call)).toEqual(["POST /nodes/builder/drain"]))
})

// Scenario: the owner removes a machine that still runs a Dispatch. Nothing is sent before the confirmation;
// the server refuses, and its reason is shown.
test("a refused removal shows the server's message", async () => {
  const user = userEvent.setup()
  const requests = nodesServer([node("builder")], {
    "DELETE /nodes/builder": () =>
      json({ code: "conflict", message: "the node still runs 1 Dispatch" }, 409),
  })
  await open()

  await user.click(await screen.findByRole("button", { name: "Actions for builder" }))
  await user.click(await screen.findByRole("menuitem", { name: "Remove" }))

  const confirm = await screen.findByRole("alertdialog")
  expect(confirm).toHaveTextContent("Remove builder?")
  expect(requests).toEqual([])

  await user.click(within(confirm).getByRole("button", { name: "Remove" }))

  expect(await within(confirm).findByRole("alert")).toHaveTextContent(
    "the node still runs 1 Dispatch",
  )
})

// Scenario: no machine yet.
test("without machines the page offers to add one", async () => {
  nodesServer([])
  await open()

  expect(await screen.findByText(/No machines yet/)).toBeInTheDocument()
  expect(screen.getAllByRole("button", { name: "Add machine" }).length).toBeGreaterThan(0)
})

const aws = (machines: number): MachineProvider => ({
  id: "mpr_1",
  name: "aws",
  kind: "aws",
  region: "ap-southeast-1",
  size: "t3.medium",
  max_machines: 2,
  machines,
})

const atAws = (state: NonNullable<Node["machine"]>["state"], error?: string): Partial<Node> => ({
  connected: false,
  system: undefined,
  stats: undefined,
  last_seen_at: undefined,
  runtimes: [],
  machine: { provider_id: "mpr_1", region: "ap-southeast-1", size: "t3.medium", state, error },
})

// Scenario: machines created at a provider that are not ready yet. One still being created is not called
// offline; one the provider refused says why; one whose removal failed stays listed with the reason. The header
// counts them apart from the machines that are simply online or offline.
test("a provider's machine says where it is until it is ready", async () => {
  nodesServer([
    node("fresh", atAws("provisioning")),
    node("refused", atAws("provision_failed", "VcpuLimitExceeded")),
    node("stuck", atAws("delete_failed", "RequestLimitExceeded")),
    node("ready", { machine: { ...atAws("ready").machine! } }),
  ])
  await open()

  const fresh = await machine("fresh")
  expect(fresh).toHaveTextContent("Creating")
  expect(fresh).not.toHaveTextContent("Offline")
  expect(fresh).toHaveTextContent("t3.mediumap-southeast-1")

  expect(await machine("refused")).toHaveTextContent("Not created")
  expect(await machine("refused")).toHaveTextContent("VcpuLimitExceeded")
  expect(await machine("stuck")).toHaveTextContent("Not removed")
  expect(await machine("stuck")).toHaveTextContent("RequestLimitExceeded")
  expect(await machine("ready")).toHaveTextContent("Online")

  expect(screen.getByText("1 online · 1 creating · 1 not created · 1 not removed")).toBeVisible()
})

// Scenario: the server has a machine provider. The owner picks it in the dialog, sees what a machine there will
// be, and creates one: the request names the provider, and the dialog says the machine is being created and
// shows no token, because the machine gets its own.
test("a new machine is created at a provider", async () => {
  const user = userEvent.setup()
  const requests = nodesServer(
    [node("builder")],
    { "POST /nodes": () => json({ node: node("build-1", atAws("provisioning")) }, 201) },
    [aws(1)],
  )
  await open()

  await user.click(screen.getByRole("button", { name: "Add machine" }))
  const dialog = await screen.findByRole("dialog")
  const choice = await within(dialog).findByRole("radio", { name: "A new machine at aws" })
  expect(choice).toHaveAccessibleDescription("t3.medium in ap-southeast-1 · 1 of 2 machines")
  expect(within(dialog).getByRole("radio", { name: "A machine of your own" })).toBeChecked()

  await user.click(choice)
  await user.type(within(dialog).getByLabelText("Name"), "build-1")
  await user.click(within(dialog).getByRole("button", { name: "Create" }))

  expect(await within(dialog).findByText("build-1 is being created")).toBeVisible()
  expect(within(dialog).queryByLabelText("Token")).not.toBeInTheDocument()
  expect(requests).toEqual([{ call: "POST /nodes", body: { name: "build-1", provider: "aws" } }])
})

// Scenario: the provider refuses the machine, or already has as many as it may. A refusal is shown in the
// provider's words with what to do next; at the limit nothing can be asked for.
test("a refused machine says why, and a provider at its limit creates none", async () => {
  const user = userEvent.setup()
  nodesServer(
    [node("builder")],
    {
      "POST /nodes": () =>
        json({ node: node("build-1", atAws("provision_failed", "VcpuLimitExceeded")) }, 201),
    },
    [aws(1), { ...aws(2), id: "mpr_2", name: "aws-full" }],
  )
  await open()

  await user.click(screen.getByRole("button", { name: "Add machine" }))
  const dialog = await screen.findByRole("dialog")
  await user.type(within(dialog).getByLabelText("Name"), "build-1")

  await user.click(await within(dialog).findByRole("radio", { name: "A new machine at aws-full" }))
  expect(dialog).toHaveTextContent("aws-full is at its limit of 2 machines")
  expect(within(dialog).getByRole("button", { name: "Create" })).toBeDisabled()

  await user.click(within(dialog).getByRole("radio", { name: "A new machine at aws" }))
  await user.click(within(dialog).getByRole("button", { name: "Create" }))

  expect(await within(dialog).findByRole("alert")).toHaveTextContent("VcpuLimitExceeded")
  expect(dialog).toHaveTextContent("build-1 was not created")
})

// Scenario: removing a provider's machine destroys it, and the confirmation says so.
test("removing a provider's machine warns that it is destroyed", async () => {
  const user = userEvent.setup()
  nodesServer([node("build-1", { machine: { ...atAws("ready").machine! } })])
  await open()

  await user.click(await screen.findByRole("button", { name: "Actions for build-1" }))
  await user.click(await screen.findByRole("menuitem", { name: "Remove" }))

  expect(await screen.findByRole("alertdialog")).toHaveTextContent(
    "Its machine is destroyed at the provider",
  )
})
