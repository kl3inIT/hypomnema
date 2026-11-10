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

import type { Secret } from "@/api/generated/types.gen"

import { VaultScreen } from "./vault-screen"

beforeEach(() => {
  // The router scrolls to the top when it opens a page; jsdom has no scrolling.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const secret = (name: string, m: Partial<Secret> = {}): Secret => ({
  name,
  kind: "generic",
  version: 1,
  used_by: [],
  key_missing: false,
  login_required: false,
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  ...m,
})

type Body = { value?: string; values?: Record<string, string> }

// The server: the vault with its values, kept apart from the list as the real one keeps them. Every request is
// kept, reads of a value included; refuse names requests it turns down.
function vault(
  start: { secrets: Secret[]; values: Record<string, Record<string, string>> },
  refuse: Record<string, [number, string]> = {},
) {
  let secrets = start.secrets
  const values = { ...start.values }
  const requests: { call: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = decodeURIComponent(new URL(request.url).pathname.replace("/api/v1", ""))
    const call = `${request.method} ${path}`
    if (call === "GET /secrets") return json({ data: secrets })

    const body = (await request
      .clone()
      .json()
      .catch(() => undefined)) as Body | undefined
    requests.push({ call, body })

    const refused = refuse[call]
    if (refused) return json({ code: "refused", message: refused[1] }, refused[0])

    const [, , name, sub] = path.split("/")
    if (!name) return json({ code: "not_found", message: "not found" }, 404)

    if (sub === "value") return json({ name, values: values[name] })

    if (request.method === "DELETE") {
      secrets = secrets.filter((s) => s.name !== name)

      return new Response(null, { status: 204 })
    }

    const old = secrets.find((s) => s.name === name)
    const kept = secret(name, { ...old, version: (old?.version ?? 0) + 1 })
    values[name] = body?.values ?? { value: body?.value ?? "" }
    secrets = [...secrets.filter((s) => s.name !== name), kept]

    return json(kept)
  })

  return requests
}

async function open() {
  const router = createRouter({
    routeTree: createRootRoute({ component: VaultScreen }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  await screen.findByRole("heading", { name: "Vault" })
}

const row = (name: string) => screen.findByRole("row", { name })

const two = () => ({
  secrets: [secret("conn-github-com-work", { version: 2 }), secret("openai-key")],
  values: {
    "conn-github-com-work": { value: "ghp_work_token" },
    "openai-key": { value: "sk-live-key" },
  },
})

// Scenario: the vault holds an API key and a GitHub token. The table names them and shows no value; the server
// is asked for a value only when the owner reveals that row, and the value leaves the page when he hides it.
test("a value is read only when the owner reveals its row", async () => {
  const user = userEvent.setup()
  const requests = vault(two())
  await open()

  const github = await row("conn-github-com-work")
  expect(github).toHaveTextContent("Generic")
  expect(document.body).not.toHaveTextContent("ghp_work_token")
  expect(requests).toEqual([])

  await user.click(within(github).getByRole("button", { name: "Reveal conn-github-com-work" }))

  expect(await within(github).findByRole("status", { name: "value" })).toHaveTextContent(
    "ghp_work_token",
  )
  expect(requests).toEqual([{ call: "GET /secrets/conn-github-com-work/value", body: undefined }])
  expect(document.body).not.toHaveTextContent("sk-live-key")

  await user.click(within(github).getByRole("button", { name: "Hide conn-github-com-work" }))
  expect(document.body).not.toHaveTextContent("ghp_work_token")
})

// Scenario: "Reveal values" in the table's head shows every row's value at once, and hides them again.
test("every value is revealed and hidden from the table's head", async () => {
  const user = userEvent.setup()
  const requests = vault(two())
  await open()
  await row("openai-key")

  await user.click(screen.getByRole("button", { name: "Reveal values" }))

  expect(await screen.findByText("ghp_work_token")).toBeInTheDocument()
  expect(await screen.findByText("sk-live-key")).toBeInTheDocument()
  expect(requests.map((r) => r.call).toSorted()).toEqual([
    "GET /secrets/conn-github-com-work/value",
    "GET /secrets/openai-key/value",
  ])

  await user.click(screen.getByRole("button", { name: "Hide values" }))
  expect(document.body).not.toHaveTextContent("sk-live-key")
})

// Scenario: the owner looks for a secret by name, and opens a row to see its version and dates.
test("the table is searched by name and a row opens to its details", async () => {
  const user = userEvent.setup()
  vault(two())
  await open()
  await row("openai-key")

  await user.type(screen.getByRole("searchbox", { name: "Search secrets by name" }), "github")
  expect(screen.queryByRole("row", { name: "openai-key" })).toBeNull()

  await user.click(screen.getByRole("button", { name: "Details of conn-github-com-work" }))
  expect(screen.getByText("Version").nextElementSibling).toHaveTextContent("2")

  await user.clear(screen.getByRole("searchbox", { name: "Search secrets by name" }))
  await user.type(screen.getByRole("searchbox", { name: "Search secrets by name" }), "nothing")
  expect(screen.getByText(/No secret matches/)).toBeInTheDocument()
})

// Scenario: a token set has several values; each is shown under its key, and it is replaced with a complete set.
test("a token set shows each value and is replaced as a set", async () => {
  const user = userEvent.setup()
  const requests = vault({
    secrets: [secret("google-work", { kind: "oauth_token_set" })],
    values: { "google-work": { refresh_token: "rt-1", access_token: "at-1" } },
  })
  await open()

  const google = await row("google-work")
  expect(google).toHaveTextContent("OAuth token set")

  await user.click(within(google).getByRole("button", { name: "Reveal google-work" }))
  expect(await within(google).findByRole("status", { name: "refresh_token" })).toHaveTextContent(
    "rt-1",
  )
  expect(within(google).getByRole("status", { name: "access_token" })).toHaveTextContent("at-1")

  await user.click(within(google).getByRole("button", { name: "Edit google-work" }))
  const dialog = within(await screen.findByRole("dialog"))
  expect(dialog.getByText(/complete replacement set/)).toBeInTheDocument()
  expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled()
  await user.type(dialog.getByLabelText("refresh_token"), "rt-2")
  await user.click(dialog.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  // The row is still revealed, so it reads the new version's value after the save.
  expect(requests.filter((r) => r.call.startsWith("PUT"))).toEqual([
    { call: "PUT /secrets/google-work", body: { values: { refresh_token: "rt-2" } } },
  ])
  expect(
    await within(await row("google-work")).findByRole("status", { name: "value" }),
  ).toHaveTextContent("rt-2")
})

// Scenario: the owner adds a secret, replaces its value, and deletes it. The value is typed into a password
// field; a deletion the server refuses, because something uses the secret, says why.
test("the owner adds, edits and deletes a secret", async () => {
  const user = userEvent.setup()
  const requests = vault(
    { secrets: [secret("conn-github-com-work")], values: {} },
    {
      "DELETE /secrets/conn-github-com-work": [
        409,
        'the secret is in use: "conn-github-com-work" is used by connection github.com:work',
      ],
    },
  )
  await open()

  await user.click(screen.getByRole("button", { name: "Add secret" }))
  let dialog = within(await screen.findByRole("dialog"))
  expect(dialog.getByLabelText("Value")).toHaveAttribute("type", "password")
  await user.type(dialog.getByLabelText("Name"), "openai-key")
  await user.type(dialog.getByLabelText("Value"), "sk-first")
  await user.click(dialog.getByRole("button", { name: "Add" }))
  await row("openai-key")
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  await user.click(within(await row("openai-key")).getByRole("button", { name: "Edit openai-key" }))
  dialog = within(await screen.findByRole("dialog"))
  await user.type(dialog.getByLabelText("Value"), "sk-second")
  await user.click(dialog.getByRole("button", { name: "Save" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  await user.click(
    within(await row("conn-github-com-work")).getByRole("button", {
      name: "Delete conn-github-com-work",
    }),
  )
  dialog = within(await screen.findByRole("dialog"))
  await user.click(dialog.getByRole("button", { name: "Delete secret" }))
  expect(await dialog.findByRole("alert")).toHaveTextContent("used by connection github.com:work")
  await user.click(dialog.getByRole("button", { name: "Cancel" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  await user.click(
    within(await row("openai-key")).getByRole("button", { name: "Delete openai-key" }),
  )
  dialog = within(await screen.findByRole("dialog"))
  await user.click(dialog.getByRole("button", { name: "Delete secret" }))
  await waitFor(() => expect(screen.queryByRole("row", { name: "openai-key" })).toBeNull())

  expect(requests).toEqual([
    { call: "PUT /secrets/openai-key", body: { value: "sk-first" } },
    { call: "PUT /secrets/openai-key", body: { value: "sk-second" } },
    { call: "DELETE /secrets/conn-github-com-work", body: undefined },
    { call: "DELETE /secrets/openai-key", body: undefined },
  ])
})

// Scenario: nothing is kept yet; the page says what will be.
test("an empty vault says what it is for", async () => {
  vault({ secrets: [], values: {} })
  await open()

  expect(await screen.findByText("No secrets yet")).toBeInTheDocument()
})
