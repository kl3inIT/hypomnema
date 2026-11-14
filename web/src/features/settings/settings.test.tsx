import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { choose } from "@/test/choose"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { ConnectionAction, GitHost, Integration, Repo } from "@/api/generated/types.gen"

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
function server(
  start: { hosts?: GitHost[]; repos?: Repo[]; services?: Integration[] },
  refuse: Record<string, string> = {},
) {
  let hosts = start.hosts ?? [known("github.com", "github"), known("gitlab.com", "gitlab")]
  let repos = start.repos ?? []
  let services = start.services ?? []
  // The sign-in the owner started and has not completed: its label and what it asked for.
  let signIn: { label: string; services: string[] } | undefined
  // What agents may do with a Google account, action by action; a service that does not go through the service
  // proxy has no actions.
  let actions: ConnectionAction[] = [
    {
      id: "gmail.read",
      service: "Gmail",
      name: "Read mail",
      text: "List, search and read messages",
      kind: "reads",
      policy: "allow",
      default: "allow",
    },
    {
      id: "gmail.send",
      service: "Gmail",
      name: "Send mail",
      text: "A new message, a reply or a draft",
      kind: "needs_care",
      policy: "ask",
      default: "ask",
    },
  ]
  const requests: { call: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = decodeURIComponent(new URL(request.url).pathname.replace("/api/v1", ""))
    const call = `${request.method} ${path}`
    if (call === "GET /hosts") return json({ data: hosts })
    if (call === "GET /repos") return json({ data: repos })
    if (call === "GET /integrations") return json({ data: services })
    if (request.method === "GET" && path.endsWith("/actions")) {
      return json({ data: path.startsWith("/integrations/google/") ? actions : [] })
    }

    const body = (await request
      .clone()
      .json()
      .catch(() => undefined)) as Record<string, string> | undefined
    requests.push({ call, body })

    if (refuse[call]) return json({ code: "invalid", message: refuse[call] }, 422)

    const [, kind, name, sub] = path.split("/")
    if (kind === "integrations" && path.includes("/actions/")) {
      const id = path.split("/").at(-1)
      const asked = body as unknown as { policy: ConnectionAction["policy"] }
      actions = actions.map((a) => (a.id === id ? { ...a, policy: asked.policy } : a))

      return json(actions.find((a) => a.id === id))
    }

    if (kind === "integrations" && sub === "sign-ins") {
      const asked = body as unknown as { label: string; services: string[]; callback: string }
      if (path.endsWith("/sign-ins")) {
        signIn = { label: asked.label, services: asked.services }

        return json({
          id: "signin-1",
          url: "https://accounts.example/auth",
          expires_in_seconds: 1800,
        })
      }

      const now = new Date().toISOString()
      const connection = {
        integration: name ?? "",
        label: signIn?.label ?? "",
        account: "owner@example.com",
        services: signIn?.services ?? [],
        sign_in_required: false,
        chats: false,
        created_at: now,
        updated_at: now,
      }
      services = services.map((i) => ({
        ...i,
        connections: [...i.connections.filter((c) => c.label !== connection.label), connection],
      }))

      return json(connection)
    }

    if (kind === "integrations" && sub === "connections" && request.method === "PUT") {
      const now = new Date().toISOString()
      const connection = {
        integration: name ?? "",
        label: path.split("/")[4] ?? "",
        account: "Hypomnema",
        services: [],
        sign_in_required: false,
        chats: false,
        created_at: now,
        updated_at: now,
        // An account at an address of its own is the user name that signed in there, with what the service runs.
        ...(body?.username && { account: body.username, address: body.address, version: "6.1.0" }),
        // An app is the id it was given, never its secret.
        ...(body?.app_id && { app_id: body.app_id }),
      }
      services = services.map((i) => ({
        ...i,
        connections: [...i.connections.filter((c) => c.label !== connection.label), connection],
      }))

      return json(connection)
    }

    if (kind === "integrations" && sub === "connections" && request.method === "PATCH") {
      const label = path.split("/")[4]
      const asked = body as unknown as { chats: boolean }
      services = services.map((i) => ({
        ...i,
        connections: i.connections.map((c) =>
          c.label === label ? { ...c, chats: asked.chats } : c,
        ),
      }))

      return json(services[0]?.connections.find((c) => c.label === label))
    }

    if (kind === "integrations" && sub === "connections") {
      const label = path.split("/")[4]
      services = services.map((i) => ({
        ...i,
        connections: i.connections.filter((c) => c.label !== label),
      }))

      return json({ revoked: true })
    }

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

const cardOf = (title: HTMLElement) => title.closest<HTMLElement>("[data-testid=integration-card]")!

// card is the card whose title is exactly name.
const card = (name: string) =>
  waitFor(() => {
    const found = screen
      .queryAllByTestId("integration-card")
      .find((c) => within(c).queryByText(name) !== null)
    if (!found) throw new Error(`no card named ${name}`)

    return found
  })

// row is the row of a host in its product's card.
const row = (kind: string, host: string) => screen.findByLabelText(`${kind} host ${host}`)

// Scenario: github.com has a token; gitlab.com and the company's GitLab server have none. There is a card a
// product with a row a host, each row saying whether it has a token. The GitLab card says once, at its foot, what
// workers do without a token and where to make one; the GitHub card, whose hosts all have one, does not say it.
test("a product's card lists its hosts and says once how to connect them", async () => {
  server({
    hosts: [
      known("github.com", "github", true),
      known("gitlab.com", "gitlab"),
      { ...known("git.company.example", "gitlab"), known: false },
    ],
  })
  await open(IntegrationsScreen, "Integrations")

  const github = within(await card("GitHub"))
  expect(screen.getAllByTestId("integration-card")).toHaveLength(2)
  const githubCom = within(await row("GitHub", "github.com"))
  expect(githubCom.getByText("Connected")).toBeInTheDocument()
  expect(githubCom.getByRole("button", { name: "Replace token" })).toBeInTheDocument()
  expect(githubCom.getByRole("button", { name: "Remove token" })).toBeInTheDocument()
  expect(githubCom.queryByRole("button", { name: /^Remove github/ })).not.toBeInTheDocument()
  expect(github.queryByText(/use the login of the machine/)).not.toBeInTheDocument()

  const gitlab = within(await card("GitLab"))
  const gitlabCom = within(await row("GitLab", "gitlab.com"))
  const company = within(await row("GitLab", "git.company.example"))
  expect(gitlabCom.getByText("No token")).toBeInTheDocument()
  expect(gitlabCom.queryByRole("button", { name: "Remove token" })).not.toBeInTheDocument()
  expect(gitlabCom.queryByText("server")).not.toBeInTheDocument()
  expect(company.getByText("No token")).toBeInTheDocument()
  expect(company.getByText("server")).toBeInTheDocument()
  expect(company.getByRole("button", { name: "Remove git.company.example" })).toBeInTheDocument()

  expect(gitlab.getAllByText(/use the login of the machine/)).toHaveLength(1)
  expect(gitlab.getAllByRole("link", { name: "How to create a token" })).toHaveLength(1)
  expect(gitlab.getByRole("link", { name: "How to create a token" })).toHaveAttribute(
    "href",
    expect.stringContaining("docs.gitlab.com"),
  )
})

// Scenario: the owner gives gitlab.com a token. It is typed into a password field, sent once without the spaces
// around it, and its row then says Connected; the token is nowhere on the page.
test("the owner gives a host its token", async () => {
  const user = userEvent.setup()
  const requests = server({})
  await open(IntegrationsScreen, "Integrations")

  await user.click(
    within(await row("GitLab", "gitlab.com")).getByRole("button", { name: "Add token" }),
  )

  const dialog = within(await screen.findByRole("dialog"))
  const field = dialog.getByLabelText("Access token")
  expect(field).toHaveAttribute("type", "password")

  // Nothing is sent without a token: the form says what is missing.
  await user.click(dialog.getByRole("button", { name: "Save token" }))
  expect(await dialog.findByRole("alert")).toHaveTextContent("Paste the token.")
  expect(requests).toEqual([])

  await user.type(field, " glpat-typed ")
  await user.click(dialog.getByRole("button", { name: "Save token" }))

  expect(
    await within(await row("GitLab", "gitlab.com")).findByText("Connected"),
  ).toBeInTheDocument()
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

  await user.click(
    within(await row("GitHub", "github.com")).getByRole("button", { name: "Add token" }),
  )

  const dialog = within(await screen.findByRole("dialog"))
  await user.type(dialog.getByLabelText("Access token"), "ghp_x")
  await user.click(dialog.getByRole("button", { name: "Save token" }))

  expect(await dialog.findByRole("alert")).toHaveTextContent("the vault has no key")
})

// Scenario: the owner adds the company's GitHub Enterprise server from the GitHub card, from a URL copied from the
// browser, with its token: it runs what the card is, so the dialog does not ask. Then the owner takes the token
// away and removes the server. Each removal asks first.
test("the owner adds a company's server to its product, removes its token and removes it", async () => {
  const user = userEvent.setup()
  const requests = server({})
  await open(IntegrationsScreen, "Integrations")

  await user.click(within(await card("GitHub")).getByRole("button", { name: "Add server" }))

  let dialog = within(await screen.findByRole("dialog"))
  expect(
    dialog.getByRole("heading", { name: "Add a GitHub Enterprise server" }),
  ).toBeInTheDocument()
  expect(dialog.queryByRole("combobox")).not.toBeInTheDocument()
  await user.type(dialog.getByLabelText("Address"), "https://Git.Company.example/team/app")
  await user.type(dialog.getByLabelText("Access token"), "ghp_company")
  await user.click(dialog.getByRole("button", { name: "Add" }))

  const company = await row("GitHub", "git.company.example")
  expect(within(await card("GitHub")).getByLabelText("GitHub host git.company.example")).toBe(
    company,
  )
  expect(company).toHaveTextContent("server")
  expect(await within(company).findByText("Connected")).toBeInTheDocument()

  await user.click(within(company).getByRole("button", { name: "Remove token" }))
  dialog = within(await screen.findByRole("alertdialog"))
  expect(dialog.getByText(/keeps the token it started with/)).toBeInTheDocument()
  await user.click(dialog.getByRole("button", { name: "Remove token" }))
  expect(
    await within(await row("GitHub", "git.company.example")).findByText("No token"),
  ).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())

  await user.click(screen.getByRole("button", { name: "Remove git.company.example" }))
  dialog = within(await screen.findByRole("alertdialog"))
  await user.click(dialog.getByRole("button", { name: "Remove host" }))
  await waitFor(() => expect(screen.queryByText("git.company.example")).not.toBeInTheDocument())

  expect(requests).toEqual([
    { call: "PUT /hosts/git.company.example", body: { kind: "github" } },
    { call: "PUT /hosts/git.company.example/token", body: { token: "ghp_company" } },
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

const google = (over: Partial<Integration> = {}): Integration => ({
  id: "google",
  name: "Google",
  kind: "oauth",
  client_id: "1234.apps.example",
  services: ["gmail", "calendar", "drive"],
  connections: [],
  ...over,
})

// Scenario: Google has its OAuth client and no account yet. The owner adds his work account: he names it, lets
// Gmail only be read and leaves Drive out, opens Google's page from the link, and pastes back the address the
// browser ended on. The account then shows with its email and what it may do. He then disconnects it, after
// being told what that does.
test("the owner signs a Google account in and disconnects it", async () => {
  const user = userEvent.setup()
  const requests = server({ services: [google()] })
  await open(IntegrationsScreen, "Integrations")

  const card = async () => within(await screen.findByText("Google").then(cardOf))
  expect((await card()).getByText("No account")).toBeInTheDocument()

  await user.click((await card()).getByRole("button", { name: "Add account" }))
  const dialog = within(await screen.findByRole("dialog"))

  // A name the server would refuse is caught before a sign-in is started.
  await user.type(dialog.getByLabelText("Name"), "My Work")
  await user.click(dialog.getByRole("button", { name: "Continue" }))
  expect(await dialog.findByRole("alert")).toHaveTextContent(
    "Lower-case letters, digits and dashes",
  )
  expect(requests).toEqual([])

  await user.clear(dialog.getByLabelText("Name"))
  await user.type(dialog.getByLabelText("Name"), "work")
  await choose(user, dialog.getByLabelText("Gmail"), "Read only")
  await choose(user, dialog.getByLabelText("Drive"), "Not connected")
  await user.click(dialog.getByRole("button", { name: "Continue" }))

  expect(await dialog.findByRole("link", { name: /Open Google's sign-in page/ })).toHaveAttribute(
    "href",
    "https://accounts.example/auth",
  )
  await user.type(
    dialog.getByLabelText("Address the browser ended on"),
    "http://localhost:1/?state=s&code=c",
  )
  await user.click(dialog.getByRole("button", { name: "Connect" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  const account = within(await screen.findByLabelText("Google account work"))
  expect(account.getByText("owner@example.com")).toBeInTheDocument()
  expect(account.getByText("Gmail · read only")).toBeInTheDocument()
  expect(account.getByText("Calendar")).toBeInTheDocument()
  expect(account.queryByText("Drive")).not.toBeInTheDocument()
  expect((await card()).getByText("Connected")).toBeInTheDocument()

  // No agent uses the account until the owner says so.
  const chats = account.getByRole("switch", { name: "Use in chats" })
  expect(chats).not.toBeChecked()
  await user.click(chats)
  await waitFor(() =>
    expect(
      within(screen.getByLabelText("Google account work")).getByRole("switch", {
        name: "Use in chats",
      }),
    ).toBeChecked(),
  )

  // What agents may do with it, action by action: reading runs on its own, sending asks. The owner forbids
  // sending, and the line under the account says so.
  const may = await screen.findByRole("button", { name: "What agents may do with work" })
  expect(may).toHaveTextContent("1 on their own · ask before 1")
  await user.click(may)

  const sheet = within(await screen.findByRole("dialog"))
  expect(sheet.getByText("A new message, a reply or a draft")).toBeInTheDocument()
  const send = within(sheet.getByRole("group", { name: "Send mail" }))
  expect(send.getByRole("button", { name: "Ask" })).toHaveAttribute("aria-pressed", "true")
  await user.click(send.getByRole("button", { name: "Never" }))
  await waitFor(() =>
    expect(
      within(sheet.getByRole("group", { name: "Send mail" })).getByRole("button", {
        name: "Never",
      }),
    ).toHaveAttribute("aria-pressed", "true"),
  )
  await user.keyboard("{Escape}")
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  expect(screen.getByRole("button", { name: "What agents may do with work" })).toHaveTextContent(
    "1 on their own · never 1",
  )

  await user.click(
    within(screen.getByLabelText("Google account work")).getByRole("button", {
      name: "Disconnect work",
    }),
  )
  const confirm = within(await screen.findByRole("alertdialog"))
  expect(confirm.getByText(/asked to end Hypomnema's access/)).toBeInTheDocument()
  await user.click(confirm.getByRole("button", { name: "Disconnect" }))
  expect(await (await card()).findByText("No account")).toBeInTheDocument()

  expect(requests).toEqual([
    {
      call: "POST /integrations/google/sign-ins",
      body: { label: "work", services: ["gmail:read", "calendar"] },
    },
    {
      call: "POST /integrations/google/sign-ins/signin-1",
      body: { callback: "http://localhost:1/?state=s&code=c" },
    },
    { call: "PATCH /integrations/google/connections/work", body: { chats: true } },
    {
      call: "PUT /integrations/google/connections/work/actions/gmail.send",
      body: { policy: "never" },
    },
    { call: "DELETE /integrations/google/connections/work", body: undefined },
  ])
})

// Scenario: one Lark organization, two apps (issue #311). The owner adds his own app as "work" with its App ID and
// App Secret, as a Lark channel's are given, then a second app as "report": no link to open and nothing to paste.
// Each account shows the app's name and id and offers no sign-in again; nothing is sent without an App Secret.
// Disconnecting says the secret leaves the vault.
test("the owner adds two Lark apps with their App ID and App Secret and disconnects one", async () => {
  const user = userEvent.setup()
  const requests = server({
    services: [{ id: "lark", name: "Lark", kind: "app", services: [], connections: [] }],
  })
  await open(IntegrationsScreen, "Integrations")

  const card = async () => within(await screen.findByText("Lark").then(cardOf))
  expect((await card()).getByText("Not connected")).toBeInTheDocument()

  async function add(label: string, appId: string, secret: string) {
    await user.click((await card()).getByRole("button", { name: "Add app" }))
    const dialog = within(await screen.findByRole("dialog"))
    await user.type(dialog.getByLabelText("Name"), label)
    await user.type(dialog.getByLabelText("App ID"), appId)
    if (secret) await user.type(dialog.getByLabelText("App Secret"), secret)
    await user.click(dialog.getByRole("button", { name: "Connect" }))
  }

  await add("work", "cli_work", "")
  expect(await screen.findByText("The app's App Secret.")).toBeInTheDocument()
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }))

  await add("work", "cli_work", "work-secret")
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  await add("report", "cli_report", "report-secret")
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  const work = within(await screen.findByLabelText("Lark account work"))
  expect(work.getByText("Hypomnema")).toBeInTheDocument()
  expect(work.getByText("cli_work")).toBeInTheDocument()
  expect(work.queryByRole("button", { name: /Sign in again/ })).not.toBeInTheDocument()
  expect(
    within(screen.getByLabelText("Lark account report")).getByText("cli_report"),
  ).toBeInTheDocument()
  expect((await card()).getByText("Connected")).toBeInTheDocument()

  await user.click(work.getByRole("button", { name: "Disconnect work" }))
  const confirm = within(await screen.findByRole("alertdialog"))
  expect(confirm.getByText(/removes its secret from the vault/)).toBeInTheDocument()
  await user.click(confirm.getByRole("button", { name: "Disconnect" }))
  await waitFor(() => expect(screen.queryByLabelText("Lark account work")).not.toBeInTheDocument())

  expect(requests).toEqual([
    {
      call: "PUT /integrations/lark/connections/work",
      body: { app_id: "cli_work", app_secret: "work-secret" },
    },
    {
      call: "PUT /integrations/lark/connections/report",
      body: { app_id: "cli_report", app_secret: "report-secret" },
    },
    { call: "DELETE /integrations/lark/connections/work", body: undefined },
  ])
})

// Scenario: Google has no OAuth client yet: the card says how to give one, and offers no sign-in. An account
// whose sign-in Google refused says so, and is signed in again under its own name with the access it had.
test("a service says what it still needs", async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  server({ services: [google({ client_id: undefined })] })
  await open(IntegrationsScreen, "Integrations")

  const bare = within(await screen.findByText("Google").then(cardOf))
  expect(bare.getByText("Not set up")).toBeInTheDocument()
  expect(bare.getByText(/integrations client set google/)).toBeInTheDocument()
  expect(bare.queryByRole("button", { name: "Add account" })).not.toBeInTheDocument()

  vi.restoreAllMocks()
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
  document.body.innerHTML = ""
  server({
    services: [
      google({
        connections: [
          {
            integration: "google",
            label: "work",
            account: "owner@example.com",
            services: ["gmail:read"],
            sign_in_required: true,
            chats: false,
            created_at: now,
            updated_at: now,
          },
        ],
      }),
    ],
  })
  await open(IntegrationsScreen, "Integrations")

  const account = within(await screen.findByLabelText("Google account work"))
  expect(account.getByText("Needs sign-in")).toBeInTheDocument()

  await user.click(account.getByRole("button", { name: "Sign in again" }))
  const dialog = within(await screen.findByRole("dialog"))
  expect(dialog.queryByLabelText("Name")).not.toBeInTheDocument()
  expect(dialog.getByLabelText("Gmail")).toHaveTextContent("Read only")
  expect(dialog.getByLabelText("Calendar")).toHaveTextContent("Not connected")
})

// Scenario: the owner connects his company's Superset. It needs no client: the card offers to add an account at
// once. The password is typed into a password field. A sign-in that Superset refuses is shown in the dialog,
// which stays open with what was typed, and nothing is kept.
test("the owner connects a Superset account with its address, a user name and a password", async () => {
  const user = userEvent.setup()
  const superset: Integration = {
    id: "superset",
    name: "Superset",
    kind: "account",
    services: [],
    connections: [],
  }
  server(
    { services: [superset] },
    { "PUT /integrations/superset/connections/vmaps": "the sign-in did not complete" },
  )
  await open(IntegrationsScreen, "Integrations")

  const card = async () => within(await screen.findByText("Superset").then(cardOf))
  expect((await card()).getByText("No account")).toBeInTheDocument()
  expect((await card()).queryByText(/integrations client set/)).not.toBeInTheDocument()

  await user.click((await card()).getByRole("button", { name: "Add account" }))
  const dialog = within(await screen.findByRole("dialog"))
  await user.click(dialog.getByRole("button", { name: "Connect" }))
  expect(await dialog.findByText(/An address such as/)).toBeInTheDocument()

  await user.type(dialog.getByLabelText("Name"), "vmaps")
  await user.type(dialog.getByLabelText("Address"), "https://superset.example.com")
  await user.type(dialog.getByLabelText("User name"), "hypomnema-reader")
  expect(dialog.getByLabelText("Password")).toHaveAttribute("type", "password")
  await user.type(dialog.getByLabelText("Password"), "a-wrong-password")
  await user.click(dialog.getByRole("button", { name: "Connect" }))
  expect(await dialog.findByText("the sign-in did not complete")).toBeInTheDocument()
  expect((await card()).getByText("No account")).toBeInTheDocument()
  expect(dialog.getByLabelText("Address")).toHaveValue("https://superset.example.com")
})

// Scenario: with the right password the account shows its user name, its address, Superset's version and that
// the last sign-in worked; the password was sent once and is nowhere on the page. Connecting again starts from
// what the account has; disconnecting says what is removed.
test("a connected Superset account shows where it is and that its sign-in works", async () => {
  const user = userEvent.setup()
  const requests = server({
    services: [
      { id: "superset", name: "Superset", kind: "account", services: [], connections: [] },
    ],
  })
  await open(IntegrationsScreen, "Integrations")

  const card = async () => within(await screen.findByText("Superset").then(cardOf))
  await user.click((await card()).getByRole("button", { name: "Add account" }))
  let dialog = within(await screen.findByRole("dialog"))
  await user.type(dialog.getByLabelText("Name"), "vmaps")
  await user.type(dialog.getByLabelText("Address"), "https://superset.example.com")
  await user.type(dialog.getByLabelText("User name"), "hypomnema-reader")
  await user.type(dialog.getByLabelText("Password"), "the-password")
  await user.click(dialog.getByRole("button", { name: "Connect" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  const account = within(await screen.findByLabelText("Superset account vmaps"))
  expect(account.getByText("hypomnema-reader")).toBeInTheDocument()
  expect(account.getByText("https://superset.example.com")).toBeInTheDocument()
  expect(account.getByText("Superset 6.1.0")).toBeInTheDocument()
  expect(account.getByText("The last sign-in worked")).toBeInTheDocument()
  expect(account.getByRole("switch", { name: "Use in chats" })).not.toBeChecked()
  expect((await card()).getByText("Connected")).toBeInTheDocument()
  expect(document.body).not.toHaveTextContent("the-password")

  // Connecting again keeps the name and starts from the address and the user name; the password is typed again.
  await user.click(account.getByRole("button", { name: "Connect again" }))
  dialog = within(await screen.findByRole("dialog"))
  expect(dialog.queryByLabelText("Name")).not.toBeInTheDocument()
  expect(dialog.getByLabelText("Address")).toHaveValue("https://superset.example.com")
  expect(dialog.getByLabelText("Password")).toHaveValue("")
  await user.click(dialog.getByRole("button", { name: "Cancel" }))

  await user.click(account.getByRole("button", { name: "Disconnect vmaps" }))
  const confirm = within(await screen.findByRole("alertdialog"))
  expect(confirm.getByText(/removes its password from the vault/)).toBeInTheDocument()
  await user.click(confirm.getByRole("button", { name: "Disconnect" }))
  expect(await (await card()).findByText("No account")).toBeInTheDocument()

  expect(requests).toEqual([
    {
      call: "PUT /integrations/superset/connections/vmaps",
      body: {
        address: "https://superset.example.com",
        username: "hypomnema-reader",
        password: "the-password",
      },
    },
    { call: "DELETE /integrations/superset/connections/vmaps", body: undefined },
  ])
})

// Scenario: the account's password was changed at Superset, so the last sign-in was refused: the account and
// its card say so.
test("a Superset account whose sign-in was refused says so", async () => {
  const now = new Date().toISOString()
  server({
    services: [
      {
        id: "superset",
        name: "Superset",
        kind: "account",
        services: [],
        connections: [
          {
            integration: "superset",
            label: "vmaps",
            account: "hypomnema-reader",
            address: "https://superset.example.com",
            version: "6.1.0",
            services: [],
            sign_in_required: true,
            chats: true,
            created_at: now,
            updated_at: now,
          },
        ],
      },
    ],
  })
  await open(IntegrationsScreen, "Integrations")

  const account = within(await screen.findByLabelText("Superset account vmaps"))
  expect(account.getByText("Superset refused the last sign-in")).toBeInTheDocument()
  expect(account.getByRole("switch", { name: "Use in chats" })).toBeChecked()
  expect(
    within(await screen.findByText("Superset").then(cardOf)).getAllByText("Needs sign-in").length,
  ).toBeGreaterThan(0)
})

test("a host and a name are read from what was typed or pasted", () => {
  expect(hostOf(" https://Git.Company.example:8443/team/app ")).toBe("git.company.example:8443")
  expect(hostOf("gitlab.company.example")).toBe("gitlab.company.example")
  expect(hostOf("")).toBe("")
  expect(nameOf("owner/Repo")).toBe("repo")
  expect(nameOf("https://gitlab.com/group/sub/app.git/")).toBe("app")
  expect(nameOf("")).toBe("")
})
