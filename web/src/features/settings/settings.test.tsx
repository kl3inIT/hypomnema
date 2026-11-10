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

import type { GitHost, Repo } from "@/api/generated/types.gen"

import { IntegrationsScreen } from "./integrations-screen"
import { RepositoriesScreen } from "./repositories-screen"
import { hostOf, nameOf } from "./use-integrations"

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

const known = (host: string, kind: GitHost["kind"], has_token = false): GitHost => ({
  host,
  kind,
  known: true,
  has_token,
  connections: has_token ? ["default"] : [],
})

// The server: the hosts and repositories it has now. A changing request is kept and applied, so that what the
// page shows next is what the server says, not what the page assumed. refuse names requests it turns down.
function server(start: { hosts?: GitHost[]; repos?: Repo[] }, refuse: Record<string, string> = {}) {
  let hosts = start.hosts ?? [known("github.com", "github"), known("gitlab.com", "gitlab")]
  let repos = start.repos ?? []
  const requests: { call: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = decodeURIComponent(new URL(request.url).pathname.replace("/api/v1", ""))
    const call = `${request.method} ${path}`
    if (call === "GET /hosts") return json({ data: hosts })
    if (call === "GET /repos") return json({ data: repos })

    const body = (await request
      .clone()
      .json()
      .catch(() => undefined)) as Record<string, string> | undefined
    requests.push({ call, body })

    if (refuse[call]) return json({ code: "invalid", message: refuse[call] }, 422)

    const [, kind, name, sub] = path.split("/")
    if (kind === "hosts" && name) {
      const found = hosts.find((h) => h.host === name)
      if (sub === "token") {
        hosts = hosts.map((h) =>
          h.host === name ? { ...h, has_token: request.method === "PUT" } : h,
        )
      } else if (request.method === "DELETE") {
        hosts = hosts.filter((h) => h.host !== name)
      } else if (!found) {
        hosts = [
          ...hosts,
          {
            host: name,
            kind: body?.kind as GitHost["kind"],
            known: false,
            has_token: false,
            connections: [],
          },
        ]
      }

      return request.method === "DELETE"
        ? new Response(null, { status: 204 })
        : json(hosts.find((h) => h.host === name))
    }

    if (kind === "repos" && name) {
      repos = repos.filter((r) => r.name !== name)
      if (request.method === "DELETE") return new Response(null, { status: 204 })

      const added: Repo = {
        name,
        host: "gitlab.company.example",
        path: "team/app",
        base: body?.base,
        created_at: new Date().toISOString(),
      }
      repos = [...repos, added]

      return json(added)
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return requests
}

async function open(Screen: () => React.JSX.Element, heading: string) {
  const router = createRouter({
    routeTree: createRootRoute({ component: Screen }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  await screen.findByRole("heading", { name: heading })
}

// card is the card whose title is exactly name.
const card = (name: string) =>
  waitFor(() => {
    const found = screen
      .queryAllByTestId("integration-card")
      .find((c) => within(c).queryByText(name) !== null)
    if (!found) throw new Error(`no card named ${name}`)

    return found
  })

// Scenario: github.com has a token and gitlab.com has none. Each card says so; the one without a token says what
// workers do meanwhile and where to make a token, and only the one with a token offers to remove it.
test("a host says whether it has a token", async () => {
  server({ hosts: [known("github.com", "github", true), known("gitlab.com", "gitlab")] })
  await open(IntegrationsScreen, "Integrations")

  const github = within(await card("GitHub"))
  expect(github.getByText("Connected")).toBeInTheDocument()
  expect(github.getByRole("button", { name: "Replace token" })).toBeInTheDocument()
  expect(github.getByRole("button", { name: "Remove token" })).toBeInTheDocument()
  expect(github.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument()

  const gitlab = within(await card("GitLab"))
  expect(gitlab.getByText("No token")).toBeInTheDocument()
  expect(gitlab.getByText(/use the login of the machine/)).toBeInTheDocument()
  expect(gitlab.getByRole("link", { name: "How to create a token" })).toHaveAttribute(
    "href",
    expect.stringContaining("docs.gitlab.com"),
  )
  expect(gitlab.queryByRole("button", { name: "Remove token" })).not.toBeInTheDocument()
})

// Scenario: the owner gives gitlab.com a token. It is typed into a password field, sent once without the spaces
// around it, and the card then says Connected; the token is nowhere on the page.
test("the owner gives a host its token", async () => {
  const user = userEvent.setup()
  const requests = server({})
  await open(IntegrationsScreen, "Integrations")

  await user.click(within(await card("GitLab")).getByRole("button", { name: "Add token" }))

  const dialog = within(await screen.findByRole("dialog"))
  const field = dialog.getByLabelText("Access token")
  expect(field).toHaveAttribute("type", "password")

  // Nothing is sent without a token: the form says what is missing.
  await user.click(dialog.getByRole("button", { name: "Save token" }))
  expect(await dialog.findByRole("alert")).toHaveTextContent("Paste the token.")
  expect(requests).toEqual([])

  await user.type(field, " glpat-typed ")
  await user.click(dialog.getByRole("button", { name: "Save token" }))

  expect(await within(await card("GitLab")).findByText("Connected")).toBeInTheDocument()
  expect(requests).toEqual([
    { call: "PUT /hosts/gitlab.com/token", body: { token: "glpat-typed" } },
  ])
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  expect(document.body).not.toHaveTextContent("glpat-typed")
})

// Scenario: the server refuses a token. The dialog stays open and says why.
test("a refused token is explained in the dialog", async () => {
  const user = userEvent.setup()
  server({}, { "PUT /hosts/github.com/token": "the vault has no key" })
  await open(IntegrationsScreen, "Integrations")

  await user.click(within(await card("GitHub")).getByRole("button", { name: "Add token" }))

  const dialog = within(await screen.findByRole("dialog"))
  await user.type(dialog.getByLabelText("Access token"), "ghp_x")
  await user.click(dialog.getByRole("button", { name: "Save token" }))

  expect(await dialog.findByRole("alert")).toHaveTextContent("the vault has no key")
})

// Scenario: the owner adds the company's GitLab server from a URL copied from the browser, with its token, then
// takes the token away, then removes the server. Each removal asks first.
test("the owner adds a company's server, removes its token and removes it", async () => {
  const user = userEvent.setup()
  const requests = server({})
  await open(IntegrationsScreen, "Integrations")

  await user.click(screen.getByRole("button", { name: "Add a git server" }))

  let dialog = within(await screen.findByRole("dialog"))
  await user.type(dialog.getByLabelText("Address"), "https://Git.Company.example/team/app")
  await user.type(dialog.getByLabelText("Access token"), "glpat-company")
  await user.click(dialog.getByRole("button", { name: "Add" }))

  const company = await card("git.company.example")
  expect(company).toHaveTextContent("GitLab server")
  expect(await within(company).findByText("Connected")).toBeInTheDocument()

  await user.click(within(company).getByRole("button", { name: "Remove token" }))
  dialog = within(await screen.findByRole("alertdialog"))
  expect(dialog.getByText(/keeps the token it started with/)).toBeInTheDocument()
  await user.click(dialog.getByRole("button", { name: "Remove token" }))
  expect(await within(await card("git.company.example")).findByText("No token")).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())

  await user.click(
    within(await card("git.company.example")).getByRole("button", { name: "Remove" }),
  )
  dialog = within(await screen.findByRole("alertdialog"))
  await user.click(dialog.getByRole("button", { name: "Remove host" }))
  await waitFor(() => expect(screen.queryByText("git.company.example")).not.toBeInTheDocument())

  expect(requests).toEqual([
    { call: "PUT /hosts/git.company.example", body: { kind: "gitlab" } },
    { call: "PUT /hosts/git.company.example/token", body: { token: "glpat-company" } },
    { call: "DELETE /hosts/git.company.example/token", body: undefined },
    { call: "DELETE /hosts/git.company.example", body: undefined },
  ])
})

// Scenario: no repository is registered; the page says what registering is for. The owner adds one from its URL:
// the name is proposed from the URL and can be changed. Then removes it, after being asked.
test("the owner registers a repository under a name and removes it", async () => {
  const user = userEvent.setup()
  const requests = server({})
  await open(RepositoriesScreen, "Repositories")

  expect(await screen.findByText("No repositories yet")).toBeInTheDocument()

  await user.click(screen.getByRole("button", { name: "Add repository" }))

  let dialog = within(await screen.findByRole("dialog"))
  await user.type(
    dialog.getByLabelText("Repository"),
    "https://gitlab.company.example/team/App.git",
  )
  expect(dialog.getByLabelText("Name")).toHaveValue("app")

  await user.clear(dialog.getByLabelText("Name"))
  await user.type(dialog.getByLabelText("Name"), "shop")
  await user.type(dialog.getByLabelText("Base branch"), "develop")
  await user.click(dialog.getByRole("button", { name: "Add" }))

  const row = (await screen.findByText("shop")).closest<HTMLElement>("[data-slot=item]")!
  expect(row).toHaveTextContent("gitlab.company.example/team/app · starts from develop")

  await user.click(within(row).getByRole("button", { name: "Remove shop" }))
  dialog = within(await screen.findByRole("alertdialog"))
  await user.click(dialog.getByRole("button", { name: "Remove repository" }))
  expect(await screen.findByText("No repositories yet")).toBeInTheDocument()

  expect(requests).toEqual([
    {
      call: "PUT /repos/shop",
      body: { repo: "https://gitlab.company.example/team/App.git", base: "develop" },
    },
    { call: "DELETE /repos/shop", body: undefined },
  ])
})

test("a host and a name are read from what was typed or pasted", () => {
  expect(hostOf(" https://Git.Company.example:8443/team/app ")).toBe("git.company.example:8443")
  expect(hostOf("gitlab.company.example")).toBe("gitlab.company.example")
  expect(hostOf("")).toBe("")
  expect(nameOf("owner/Repo")).toBe("repo")
  expect(nameOf("https://gitlab.com/group/sub/app.git/")).toBe("app")
  expect(nameOf("")).toBe("")
})
