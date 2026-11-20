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
import { useState } from "react"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { ConnectionAction, GitHost, Integration, Repo } from "@/api/generated/types.gen"

import { IntegrationsScreen } from "./integrations-screen"
import { RepositoriesScreen } from "./repositories-screen"
import { hostOf, nameOf } from "./use-integrations"
import { SidebarProvider } from "@/components/ui/sidebar"

beforeEach(() => {
  // The router scrolls to the top when it opens a page; jsdom has no scrolling.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB8TL1wZn5vEveB+nHTVCX490uF9kH8g6jcBT6BgFep8"

const sshPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH0nodes hypomnema"

const sshUrl = "ssh://git@167.254.68.172:222/mobility/platform.git"

const repo = (name: string, host = "gitlab.company.example", over: Partial<Repo> = {}): Repo => ({
  name,
  host,
  path: host === "github.com" ? `owner/${name}` : "team/app",
  created_at: new Date().toISOString(),
  ...over,
})

// Repositories is the page as its route gives it: the host it is narrowed to, kept while the test runs.
function Repositories() {
  const [host, setHost] = useState<string>()

  return <RepositoriesScreen host={host} onHost={setHost} />
}

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
      access: "unknown",
    },
    {
      id: "gmail.send",
      service: "Gmail",
      name: "Send mail",
      text: "A new message, a reply or a draft",
      kind: "needs_care",
      policy: "ask",
      default: "ask",
      access: "unknown",
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
    if (call === "GET /git/ssh-key") return json({ public_key: sshPublicKey })
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
    if (kind === "integrations" && request.method === "POST" && path.endsWith("/check")) {
      const label = path.split("/")[4]
      actions = actions.map((a) => ({
        ...a,
        access: a.id === "gmail.send" ? "missing" : "granted",
        reason: a.id === "gmail.send" ? "gmail granted read only" : "granted gmail:read",
      }))
      const cannot = [
        {
          action: "gmail.send",
          service: "Gmail",
          name: "Send mail",
          reason: "gmail granted read only",
        },
      ]
      services = services.map((i) => ({
        ...i,
        connections: i.connections.map((c) =>
          c.label === label ? { ...c, cannot, access_checked_at: new Date().toISOString() } : c,
        ),
      }))

      return json(services.flatMap((i) => i.connections).find((c) => c.label === label))
    }

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
        cannot: [],
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
        cannot: [],
        created_at: now,
        updated_at: now,
        // An account at an address of its own is the user name that signed in there, with what the service runs.
        ...(body?.username && { account: body.username, address: body.address, version: "6.1.0" }),
        // An app is the id it was given, never its secret.
        ...(body?.app_id && { app_id: body.app_id }),
        // A service account is its client_email and the settings, never its key (issue #976).
        ...(body?.datasets && {
          account: "hypomnema-ga4@my-app.iam.gserviceaccount.com",
          bigquery: {
            billing_project: body.billing_project ?? "my-app",
            datasets: body.datasets as unknown as string[],
            max_bytes_billed: 10 * 2 ** 30,
          },
        }),
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
        ssh_url: body?.ssh_url,
        ssh_host_key: body?.ssh_host_key,
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
      <SidebarProvider>
        <RouterProvider router={router} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  await screen.findByRole("heading", { name: heading, level: 1 })
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

// Scenario: the SSH key moved to Repositories (issue #647): Integrations lists the git hosts without it. A host's
// row says how many registered repositories are on it and leads to them.
test("a git host's row leads to its repositories, and the SSH key is not under Integrations", async () => {
  server({
    hosts: [
      known("github.com", "github"),
      { ...known("gitlab.company.example", "gitlab"), known: false },
    ],
    repos: [repo("app"), repo("web")],
  })
  await open(IntegrationsScreen, "Integrations")

  const company = within(await row("GitLab", "gitlab.company.example"))
  expect(await company.findByRole("link", { name: /2 repositories/ })).toHaveAttribute(
    "href",
    "/settings/repositories?host=gitlab.company.example",
  )
  expect(within(await row("GitHub", "github.com")).queryByRole("link")).not.toBeInTheDocument()
  expect(screen.queryByText("SSH key")).not.toBeInTheDocument()
})

// Scenario: a repository is cloned over SSH when its host's HTTPS cannot carry it (issue #625). The Repositories
// page opens with the public key the nodes clone with, whole and with a button that copies it, and the hosts whose
// repositories are cloned with it, each leading to the page of that host where a key is added (issue #647).
test("the SSH key the nodes clone with heads Repositories, with the hosts that use it", async () => {
  server({
    hosts: [known("github.com", "github"), known("gitlab.company.example", "gitlab")],
    repos: [
      repo("platform", "gitlab.company.example", { ssh_url: sshUrl, ssh_host_key: hostKey }),
      repo("app"),
      repo("site", "github.com"),
    ],
  })
  await open(Repositories, "Repositories")

  const key = within(await screen.findByTestId("ssh-key"))
  expect(await key.findByText(sshPublicKey)).toBeInTheDocument()
  expect(key.getByRole("button", { name: "Copy" })).toBeInTheDocument()
  const host = within(await screen.findByLabelText("SSH host gitlab.company.example"))
  expect(host.getByText("Used by 1 repository.")).toBeInTheDocument()
  expect(host.getByRole("link", { name: /SSH keys on the host/ })).toHaveAttribute(
    "href",
    "https://gitlab.company.example/-/user_settings/ssh_keys",
  )
  expect(screen.queryByLabelText("SSH host github.com")).not.toBeInTheDocument()

  const platform = (await screen.findByText("platform")).closest<HTMLElement>("[data-slot=item]")!
  expect(within(platform).getByText("SSH")).toBeInTheDocument()
  expect(platform).toHaveTextContent("cloned over SSH from 167.254.68.172:222")
  expect(within(platform).getByRole("link", { name: "gitlab.company.example" })).toHaveAttribute(
    "href",
    "/settings/integrations",
  )
  const site = screen.getByText("site").closest<HTMLElement>("[data-slot=item]")!
  expect(within(site).getByText("HTTPS")).toBeInTheDocument()
})

// Scenario: repositories are on two hosts. The pills narrow the list to one host and back to all.
test("the owner narrows the repositories to one host", async () => {
  const user = userEvent.setup()
  server({ repos: [repo("app"), repo("web"), repo("site", "github.com")] })
  await open(Repositories, "Repositories")

  const filter = within(await screen.findByRole("group", { name: "Git host" }))
  expect(filter.getByRole("button", { name: "All 3" })).toHaveAttribute("aria-pressed", "true")
  await user.click(filter.getByRole("button", { name: "github.com 1" }))
  expect(screen.queryByText("app")).not.toBeInTheDocument()
  expect(screen.getByText("site")).toBeInTheDocument()

  await user.click(filter.getByRole("button", { name: "All 3" }))
  expect(await screen.findByText("app")).toBeInTheDocument()
})

// Scenario: a repository registered over HTTPS is moved to SSH from its row's Edit (issue #647): the dialog starts
// from what it is now, asks for the address and the host's key only once SSH is chosen, and shows the key's
// fingerprint. What is sent names the repository whole, its Connection kept, as `repos set` does.
test("the owner moves a repository to SSH from its Edit dialog", async () => {
  const user = userEvent.setup()
  const requests = server({ repos: [repo("app", undefined, { base: "main", connection: "work" })] })
  await open(Repositories, "Repositories")

  await user.click(await screen.findByRole("button", { name: "Edit app" }))
  const dialog = within(await screen.findByRole("dialog"))
  expect(dialog.getByRole("heading", { name: "Edit app" })).toBeInTheDocument()
  expect(dialog.queryByLabelText("Repository")).not.toBeInTheDocument()
  expect(dialog.getByLabelText("Base branch")).toHaveValue("main")
  expect(dialog.queryByLabelText("SSH address")).not.toBeInTheDocument()

  await user.click(dialog.getByRole("button", { name: "SSH" }))
  await user.type(dialog.getByLabelText("SSH address"), sshUrl)
  await user.click(dialog.getByRole("button", { name: "Save" }))
  expect(await dialog.findByText(/Give the SSH host's key/)).toBeInTheDocument()
  expect(requests).toEqual([])

  await user.type(dialog.getByLabelText("SSH host key"), hostKey)
  expect(await dialog.findByTestId("host-key-fingerprint")).toHaveTextContent(
    "SHA256:Ls1IQVqyE39d8qi/ZFNrZbP7SsHmSqWVrJiVjUjpJ/M (ED25519)",
  )
  await user.click(dialog.getByRole("button", { name: "Save" }))

  const row = (await screen.findByText("app")).closest<HTMLElement>("[data-slot=item]")!
  expect(await within(row).findByText("SSH")).toBeInTheDocument()
  expect(requests).toEqual([
    {
      call: "PUT /repos/app",
      body: {
        repo: "https://gitlab.company.example/team/app",
        base: "main",
        connection: "work",
        ssh_url: sshUrl,
        ssh_host_key: hostKey,
      },
    },
  ])
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
  await open(Repositories, "Repositories")

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

// Scenario: the company's server cuts a large clone over HTTPS, so the owner gives the repository its SSH address
// too (issue #625), with the host's key: a host is never trusted on first use. They are sent with the repository and
// its row says it is cloned over SSH; an address that is not one, or one without the host's key, is not sent.
test("the owner registers a repository with its SSH address", async () => {
  const user = userEvent.setup()
  const requests = server({})
  await open(Repositories, "Repositories")

  await user.click(await screen.findByRole("button", { name: "Add repository" }))

  const dialog = within(await screen.findByRole("dialog"))
  await user.type(
    dialog.getByLabelText("Repository"),
    "https://gitlab.company.example/mobility/platform",
  )
  expect(dialog.queryByLabelText("SSH address")).not.toBeInTheDocument()
  await user.click(dialog.getByRole("button", { name: "SSH" }))
  await user.type(dialog.getByLabelText("SSH address"), "https://gitlab.company.example/x.git")
  await user.click(dialog.getByRole("button", { name: "Add" }))
  expect(await dialog.findByText(/such as ssh:\/\/git@host:222/)).toBeInTheDocument()
  expect(requests).toEqual([])

  await user.clear(dialog.getByLabelText("SSH address"))
  await user.type(
    dialog.getByLabelText("SSH address"),
    "ssh://git@167.254.68.172:222/mobility/platform.git",
  )
  await user.click(dialog.getByRole("button", { name: "Add" }))
  expect(await dialog.findByText(/Give the SSH host's key/)).toBeInTheDocument()
  expect(requests).toEqual([])

  await user.type(dialog.getByLabelText("SSH host key"), hostKey)
  await user.click(dialog.getByRole("button", { name: "Add" }))

  const row = (await screen.findByText("platform")).closest<HTMLElement>("[data-slot=item]")!
  expect(row).toHaveTextContent("cloned over SSH from 167.254.68.172:222")
  expect(requests).toEqual([
    {
      call: "PUT /repos/platform",
      body: {
        repo: "https://gitlab.company.example/mobility/platform",
        ssh_url: "ssh://git@167.254.68.172:222/mobility/platform.git",
        ssh_host_key: hostKey,
      },
    },
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

// Scenario (issue #567): the owner opens what agents may do with his Google account, which was never read at
// Google. He checks it again: Gmail turns out to be granted read only. Sending stays in the list, muted, and says
// why; the line under the account counts what the account cannot do.
test("an action the account cannot do stays in the list and says why", async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const requests = server({
    services: [
      google({
        connections: [
          {
            integration: "google",
            label: "work",
            account: "owner@example.com",
            services: ["gmail:read"],
            sign_in_required: false,
            chats: true,
            cannot: [],
            created_at: now,
            updated_at: now,
          },
        ],
      }),
    ],
  })
  await open(IntegrationsScreen, "Integrations")

  await user.click(await screen.findByRole("button", { name: "What agents may do with work" }))
  const sheet = within(await screen.findByRole("dialog"))
  expect(sheet.getByText("What the account can do at Google was not read yet.")).toBeInTheDocument()
  expect(sheet.queryByText("This account cannot:")).not.toBeInTheDocument()

  await user.click(sheet.getByRole("button", { name: "Check again" }))
  expect(await sheet.findByText("This account cannot:")).toBeInTheDocument()
  expect(sheet.getByText("gmail granted read only")).toBeInTheDocument()
  expect(await sheet.findByText(/was read just now/)).toBeInTheDocument()
  // The row stays and keeps the owner's answer.
  expect(
    within(sheet.getByRole("group", { name: "Send mail" })).getByRole("button", { name: "Ask" }),
  ).toHaveAttribute("aria-pressed", "true")

  await user.keyboard("{Escape}")
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  expect(screen.getByRole("button", { name: "What agents may do with work" })).toHaveTextContent(
    "1 on their own · ask before 1 · 1 the account cannot",
  )
  expect(requests).toEqual([
    { call: "POST /integrations/google/connections/work/check", body: undefined },
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
            cannot: [],
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
            cannot: [],
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

// Scenario (issue #976): the owner adds a BigQuery account with a key file and the GA4 dataset. The key is sent
// once; only the file's name is shown, never what it holds, before or after connecting. The account shows its
// dataset, its billing project and its cap; disconnecting says the key still exists at Google.
test("the owner connects BigQuery with a key file that is never shown back", async () => {
  const user = userEvent.setup()
  const requests = server({
    services: [
      { id: "bigquery", name: "BigQuery", kind: "serviceaccount", services: [], connections: [] },
    ],
  })
  await open(IntegrationsScreen, "Integrations")

  const card = async () => within(await screen.findByText("BigQuery").then(cardOf))
  expect((await card()).getByText("Not connected")).toBeInTheDocument()
  await user.click((await card()).getByRole("button", { name: "Add key" }))
  const dialog = within(await screen.findByRole("dialog"))

  await user.click(dialog.getByRole("button", { name: "Connect" }))
  expect(await dialog.findByText("Choose the service account's JSON key file.")).toBeInTheDocument()

  const key = JSON.stringify({
    type: "service_account",
    client_email: "hypomnema-ga4@my-app.iam.gserviceaccount.com",
    private_key:
      "-----BEGIN PRIVATE KEY-----\\nnot-a-real-key-of-the-test\\n-----END PRIVATE KEY-----\\n",
  })
  await user.type(dialog.getByLabelText("Name"), "ga4")
  await user.upload(
    dialog.getByLabelText("Choose the service account's JSON key file"),
    new File([key], "my-app-key.json", { type: "application/json" }),
  )
  expect(await dialog.findByText("my-app-key.json chosen")).toBeInTheDocument()
  expect(document.body).not.toHaveTextContent("not-a-real-key-of-the-test")
  await user.type(dialog.getByLabelText("GA4 dataset"), "my-app.analytics_123")
  await user.click(dialog.getByRole("button", { name: "Connect" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  const account = within(await screen.findByLabelText("BigQuery account ga4"))
  expect(account.getByText("my-app.analytics_123")).toBeInTheDocument()
  expect(account.getByText("Billed to my-app")).toBeInTheDocument()
  expect(account.getByText("At most 10GiB a query")).toBeInTheDocument()
  expect(document.body).not.toHaveTextContent("not-a-real-key-of-the-test")
  expect(document.body).not.toHaveTextContent("PRIVATE KEY")

  // Connecting again starts from the settings, without the key: the one kept is used.
  await user.click(account.getByRole("button", { name: "Connect again" }))
  const again = within(await screen.findByRole("dialog"))
  expect(again.getByLabelText("GA4 dataset")).toHaveValue("my-app.analytics_123")
  expect(again.getByText("The key kept is used")).toBeInTheDocument()
  await user.click(again.getByRole("button", { name: "Cancel" }))

  await user.click(account.getByRole("button", { name: "Disconnect ga4" }))
  const confirm = within(await screen.findByRole("alertdialog"))
  expect(confirm.getByText(/The key still exists at Google/)).toBeInTheDocument()
  await user.click(confirm.getByRole("button", { name: "Disconnect" }))

  expect(requests[0]).toEqual({
    call: "PUT /integrations/bigquery/connections/ga4",
    body: { datasets: ["my-app.analytics_123"], service_account_key: key },
  })
})
