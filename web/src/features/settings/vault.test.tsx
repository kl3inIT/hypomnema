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
      .catch(() => undefined)) as { value?: string } | undefined
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
    values[name] = { value: body?.value ?? "" }
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

const row = (name: string) => screen.findByRole("listitem", { name })

// Scenario: the vault holds an API key and a GitHub token. The page lists them with what uses each and shows no
// value; the server is asked for a value only when the owner reveals that secret, and the value leaves the page
// when he hides it.
test("a value is read only when the owner reveals it", async () => {
  const user = userEvent.setup()
  const requests = vault({
    secrets: [
      secret("conn-github-com-work", { used_by: ["connection github.com:work"], version: 2 }),
      secret("openai-key"),
    ],
    values: {
      "conn-github-com-work": { value: "ghp_work_token" },
      "openai-key": { value: "sk-live-key" },
    },
  })
  await open()

  const github = await row("conn-github-com-work")
  expect(github).toHaveTextContent("version 2")
  expect(github).toHaveTextContent("used by connection github.com:work")
  expect(await row("openai-key")).toHaveTextContent("not used")
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

// Scenario: a token set has several values; each is shown under its key.
test("a token set shows each of its values", async () => {
  const user = userEvent.setup()
  vault({
    secrets: [secret("google-work", { kind: "oauth_token_set" })],
    values: { "google-work": { refresh_token: "rt-1", access_token: "at-1" } },
  })
  await open()

  const google = await row("google-work")
  expect(google).toHaveTextContent("Token set")
  expect(within(google).queryByRole("button", { name: "Replace google-work" })).toBeNull()

  await user.click(within(google).getByRole("button", { name: "Reveal google-work" }))

  expect(await within(google).findByRole("status", { name: "refresh_token" })).toHaveTextContent(
    "rt-1",
  )
  expect(within(google).getByRole("status", { name: "access_token" })).toHaveTextContent("at-1")
})

// Scenario: the owner adds a secret, replaces its value, and removes it. The value is typed into a password
// field; a removal the server refuses, because something uses the secret, says so.
test("the owner adds, replaces and removes a secret", async () => {
  const user = userEvent.setup()
  const requests = vault(
    {
      secrets: [secret("conn-github-com-work", { used_by: ["connection github.com:work"] })],
      values: {},
    },
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

  const added = await row("openai-key")
  expect(added).toHaveTextContent("version 1")
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  await user.click(within(added).getByRole("button", { name: "Replace openai-key" }))
  dialog = within(await screen.findByRole("dialog"))
  await user.type(dialog.getByLabelText("Value"), "sk-second")
  await user.click(dialog.getByRole("button", { name: "Replace" }))
  await waitFor(async () => expect(await row("openai-key")).toHaveTextContent("version 2"))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  await user.click(
    within(await row("conn-github-com-work")).getByRole("button", {
      name: "Remove conn-github-com-work",
    }),
  )
  dialog = within(await screen.findByRole("dialog"))
  expect(dialog.getByText(/used by connection github.com:work/)).toBeInTheDocument()
  await user.click(dialog.getByRole("button", { name: "Remove secret" }))
  expect(await dialog.findByRole("alert")).toHaveTextContent("the secret is in use")
  await user.click(dialog.getByRole("button", { name: "Cancel" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  await user.click(
    within(await row("openai-key")).getByRole("button", { name: "Remove openai-key" }),
  )
  dialog = within(await screen.findByRole("dialog"))
  await user.click(dialog.getByRole("button", { name: "Remove secret" }))
  await waitFor(() => expect(screen.queryByRole("listitem", { name: "openai-key" })).toBeNull())

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

  expect(await screen.findByText("The vault is empty")).toBeInTheDocument()
})
