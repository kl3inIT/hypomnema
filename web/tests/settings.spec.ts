import { expect, test, type Locator, type Page } from "@playwright/test"

import type { GitHost as Host, Repo } from "../src/api/generated/types.gen.js"
import { choose } from "./choose.js"
import { shot } from "./shot.js"

// Settings, built, against a fake client API, on a phone and a desktop.

const now = new Date().toISOString()

async function fakeServer(page: Page, start: { hosts?: Host[]; repos?: Repo[] } = {}) {
  let hosts: Host[] = start.hosts ?? [
    { host: "github.com", kind: "github", known: true, has_token: true, connections: ["default"] },
    { host: "gitlab.com", kind: "gitlab", known: true, has_token: false, connections: [] },
    {
      host: "git.company.example",
      kind: "gitlab",
      known: false,
      has_token: false,
      connections: [],
    },
  ]
  let repos = start.repos ?? []
  // Google, with its OAuth client and one account that was signed in.
  let accounts = [
    {
      integration: "google",
      label: "personal",
      account: "owner@example.com",
      services: ["gmail:read", "calendar"],
      sign_in_required: false,
      chats: true,
      created_at: now,
      updated_at: now,
    },
  ]
  // Lark, an app: connected with no sign-in once its id and secret were given.
  // Lark, an app Integration: each account is an app given with its App ID and App Secret (issue #311).
  let apps: ((typeof accounts)[number] & { app_id?: string })[] = []
  let asked: { label: string; services: string[] } | undefined
  // What agents may do with a Google account: the actions of the service proxy's catalog, with their answers.
  const action = (id: string, service: string, name: string, text: string, policy: string) => ({
    id,
    service,
    name,
    text,
    kind: policy === "allow" ? "reads" : "needs_care",
    policy,
    default: policy,
  })
  let actions = [
    action("gmail.read", "Gmail", "Read mail", "List, search and read messages", "allow"),
    action(
      "gmail.draft",
      "Gmail",
      "Write drafts",
      "Create and edit drafts; nothing is sent",
      "allow",
    ),
    action(
      "gmail.send",
      "Gmail",
      "Send mail",
      "A new message, a reply, a forward or a draft",
      "ask",
    ),
    action("gmail.trash", "Gmail", "Trash or delete mail", "Move a message to the trash", "ask"),
    action("calendar.read", "Calendar", "Read events", "The agenda, free and busy times", "allow"),
    action("calendar.write", "Calendar", "Create or change an event", "Guests get an email", "ask"),
    action("drive.read", "Drive", "Read files", "Search, list and download", "allow"),
    action("drive.share", "Drive", "Share a file", "Give someone access", "ask"),
  ]
  // Superset, an account at an address of its own: connected with what the owner types. Its actions are the parts
  // of Superset's API: reading runs on its own, the rest asks.
  let supersets: (typeof accounts)[number][] = []
  // AWS: a role a Connection, assumed with a key kept under a name (issue #331).
  let roles: Record<string, unknown>[] = []
  const keys = new Set<string>()
  const supersetActions = [
    action("superset.dashboards.read", "Dashboards", "Read", "Every GET", "allow"),
    action(
      "superset.dashboards.write",
      "Dashboards",
      "Create or change",
      "Creating, changing",
      "ask",
    ),
    action("superset.charts.read", "Charts", "Read", "Every GET, and a chart's data", "allow"),
    action("superset.charts.delete", "Charts", "Delete", "", "ask"),
    action(
      "superset.sql.run",
      "SQL Lab",
      "Run SQL",
      "Run SQL the agent wrote on a database",
      "ask",
    ),
  ]
  const calls: string[] = []
  let ownerMemory = "The owner writes in Vietnamese.\nShort answers, one recommendation."

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push(call)

    if (path === "/me") return json({ id: "usr_1", name: "admin", kind: "owner" })

    // The memory about the owner (issue #21), which agents wrote and the owner replaces.
    if (path === "/owner-memory") {
      if (request.method() === "PUT")
        ownerMemory = (request.postDataJSON() as { memory: string }).memory.trim()

      return json({ memory: ownerMemory, memory_limit: 1375 })
    }

    const [, kind, name, sub] = path.split("/")

    if (kind === "integrations" && path.endsWith("/actions")) {
      return json({
        data: name === "google" ? actions : name === "superset" ? supersetActions : [],
      })
    }

    if (kind === "integrations" && path.includes("/actions/")) {
      const id = path.split("/").at(-1)
      const body = request.postDataJSON() as { policy: string }
      actions = actions.map((a) => (a.id === id ? { ...a, policy: body.policy } : a))

      return json(actions.find((a) => a.id === id))
    }

    if (kind === "hosts" && name) {
      if (name === "refused.example") {
        return json({ code: "invalid", message: "the vault has no key" }, 422)
      }

      if (sub === "token") {
        hosts = hosts.map((h) =>
          h.host === name
            ? {
                ...h,
                has_token: request.method() === "PUT",
                connections: request.method() === "PUT" ? ["default"] : [],
              }
            : h,
        )
      } else if (request.method() === "DELETE") {
        hosts = hosts.filter((h) => h.host !== name)
      } else if (!hosts.some((h) => h.host === name)) {
        const body = request.postDataJSON() as { kind: Host["kind"] }
        hosts = [
          ...hosts,
          { host: name, kind: body.kind, known: false, has_token: false, connections: [] },
        ]
      }

      return request.method() === "DELETE"
        ? route.fulfill({ status: 204 })
        : json(hosts.find((h) => h.host === name))
    }

    if (kind === "integrations" && sub === "sign-ins") {
      if (path.endsWith("/sign-ins")) {
        asked = request.postDataJSON() as { label: string; services: string[] }

        return json({
          id: "signin-1",
          url: "https://accounts.example/auth",
          expires_in_seconds: 1800,
        })
      }

      const added = {
        ...accounts[0]!,
        label: asked?.label ?? "",
        services: asked?.services ?? [],
        chats: false,
      }
      accounts = [...accounts.filter((a) => a.label !== added.label), added]

      return json(added)
    }

    if (kind === "integrations" && sub === "connections" && name === "aws") {
      const label = path.split("/")[4] ?? ""

      if (request.method() === "PUT") {
        const body = request.postDataJSON() as {
          role_arn: string
          key: string
          access_key_id?: string
          secret_access_key?: string
        }
        if (body.access_key_id === undefined && !keys.has(body.key)) {
          return json(
            {
              code: "invalid_request",
              message: `invalid: there is no key "${body.key}" yet; give its access key id and secret access key`,
            },
            400,
          )
        }
        if (body.access_key_id !== undefined && body.secret_access_key !== "the-right-secret") {
          return json(
            {
              code: "invalid_request",
              message: `invalid: the sign-in did not complete: AWS did not let key ${body.key} assume ${body.role_arn}: AccessDenied: User is not authorized to perform: sts:AssumeRole`,
            },
            400,
          )
        }
        keys.add(body.key)

        const added = {
          integration: "aws",
          label,
          account: body.role_arn.split(":")[4] ?? "",
          role_arn: body.role_arn,
          key: body.key,
          services: [],
          sign_in_required: false,
          chats: false,
          created_at: now,
          updated_at: now,
        }
        roles = [...roles.filter((a) => a.label !== label), added]

        return json(added)
      }

      roles = roles.filter((a) => a.label !== label)

      return json({ revoked: true })
    }

    if (kind === "integrations" && sub === "connections" && name === "superset") {
      const label = path.split("/")[4] ?? ""

      if (request.method() === "PUT") {
        const body = request.postDataJSON() as {
          address: string
          username: string
          password: string
        }
        if (body.password !== "the-right-password") {
          return json(
            {
              code: "invalid_request",
              message:
                "invalid: the sign-in did not complete: Superset refused the user name and password, or the address is not its API: status 401",
            },
            400,
          )
        }

        const added = {
          integration: "superset",
          label,
          account: body.username,
          address: body.address,
          version: "6.1.0",
          services: [],
          sign_in_required: false,
          chats: false,
          created_at: now,
          updated_at: now,
        }
        supersets = [...supersets.filter((a) => a.label !== label), added]

        return json(added)
      }

      if (request.method() === "PATCH") {
        const asked = request.postDataJSON() as { chats: boolean }
        supersets = supersets.map((a) => (a.label === label ? { ...a, chats: asked.chats } : a))

        return json(supersets.find((a) => a.label === label))
      }

      supersets = supersets.filter((a) => a.label !== label)

      return json({ revoked: true })
    }

    if (kind === "integrations" && sub === "connections" && request.method() === "PUT") {
      const body = request.postDataJSON() as { app_id: string; app_secret: string }
      const added = {
        integration: name ?? "",
        label: path.split("/")[4] ?? "",
        // Lark answers with the app's own name.
        account: body.app_id === "cli_report" ? "Superset Report" : "Hypomnema",
        app_id: body.app_id,
        services: [],
        sign_in_required: false,
        chats: false,
        created_at: now,
        updated_at: now,
      }
      apps = [...apps.filter((a) => a.label !== added.label), added]

      return json(added)
    }

    if (kind === "integrations" && sub === "connections" && name === "lark") {
      apps = apps.filter((a) => a.label !== path.split("/")[4])

      return json({ revoked: true })
    }

    if (kind === "integrations" && sub === "connections" && request.method() === "PATCH") {
      const asked = request.postDataJSON() as { chats: boolean }
      accounts = accounts.map((a) =>
        a.label === path.split("/")[4] ? { ...a, chats: asked.chats } : a,
      )

      return json(accounts.find((a) => a.label === path.split("/")[4]))
    }

    if (kind === "integrations" && sub === "connections") {
      accounts = accounts.filter((a) => a.label !== path.split("/")[4])

      return json({ revoked: true })
    }

    if (kind === "repos" && name) {
      repos = repos.filter((r) => r.name !== name)
      if (request.method() === "DELETE") return route.fulfill({ status: 204 })

      const body = request.postDataJSON() as { repo: string; base?: string }
      const added = {
        name,
        host: "git.company.example",
        path: "team/app",
        base: body.base,
        created_at: now,
      }
      repos = [...repos, added]

      return json(added)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /hosts":
        return json({ data: hosts })
      case "GET /integrations":
        return json({
          data: [
            {
              id: "google",
              name: "Google",
              kind: "oauth",
              client_id: "1234.apps.example",
              services: ["gmail", "calendar", "drive", "docs", "sheets"],
              connections: accounts,
            },
            {
              id: "lark",
              name: "Lark",
              kind: "app",
              services: [],
              connections: apps,
            },
            {
              id: "superset",
              name: "Superset",
              kind: "account",
              services: [],
              connections: supersets,
            },
            {
              id: "aws",
              name: "AWS",
              kind: "role",
              services: [],
              connections: roles,
            },
          ],
        })
      case "GET /repos":
        return json({ data: repos })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
      case "GET /questions":
      case "GET /profiles":
        return json({ data: [] })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return calls
}

const card = (page: Page, name: string) =>
  page.getByTestId("integration-card").filter({ has: page.getByText(name, { exact: true }) })

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens Settings from the app, lands on Integrations, gives gitlab.com a token and sees it
// connected; the token is typed into a password field and is nowhere on the page afterwards.
// Scenario: the owner reads what every agent and Bot knows about him, under Agents, and adds a rule (issue #21).
test("the owner reads and edits what every agent knows about him", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/about-you")

  await expect(page.getByRole("heading", { name: "About you", level: 1 })).toBeVisible()
  const field = page.getByRole("textbox", { name: "Memory" })
  await expect(field).toHaveValue(
    "The owner writes in Vietnamese.\nShort answers, one recommendation.",
  )
  await expect(page.getByText("66 of 1,375 characters")).toBeVisible()
  await shot(page, "about-you")

  await field.fill(
    "The owner writes in Vietnamese.\nShort answers, one recommendation.\nAsk before pushing.",
  )
  await page.getByRole("button", { name: "Save memory" }).click()
  await expect(page.getByRole("button", { name: "Save memory" })).toHaveCount(0)
  expect(calls).toEqual(["PUT /owner-memory"])
})

// top shows a part of the page at the top of the screen, for a picture of it.
const top = (target: Locator) => target.evaluate((e) => e.scrollIntoView({ block: "start" }))

// Scenario: the owner opens Settings from the app and lands on Integrations. There is a card a product, a row a
// host: GitHub's one host has a token; gitlab.com and the company's GitLab server have none, and the GitLab card
// says once how to connect them. The owner gives gitlab.com a token and sees its row connected while the company's
// server is still without one; the token is typed into a password field and is nowhere on the page afterwards.
test("the owner opens Settings and gives a host its token", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/")
  await page.getByRole("link", { name: "Settings" }).click()

  await expect(page).toHaveURL(/\/settings\/integrations$/)
  await expect(page.getByRole("heading", { name: "Integrations", level: 1 })).toBeVisible()
  const github = card(page, "GitHub")
  const gitlab = card(page, "GitLab")
  const gitlabCom = gitlab.getByLabel("GitLab host gitlab.com")
  const company = gitlab.getByLabel("GitLab host git.company.example")
  await expect(github.getByLabel("GitHub host github.com")).toContainText("Connected")
  await expect(github.getByText("How to create a token")).toHaveCount(0)
  await expect(gitlabCom).toContainText("No token")
  await expect(company).toContainText("server")
  await expect(company).toContainText("No token")
  await expect(gitlab.getByText("How to create a token")).toHaveCount(1)
  expect(await fits(page)).toBe(true)
  await top(page.getByRole("heading", { name: "Git hosts" }))
  await shot(page, "git-hosts-github")
  await top(gitlab)
  await shot(page, "git-hosts-no-token")

  await gitlabCom.getByRole("button", { name: "Add token" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await expect(dialog.getByLabel("Access token")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Access token").fill("glpat-typed")
  await shot(page, "integrations-token-dialog")
  await dialog.getByRole("button", { name: "Save token" }).click()

  await expect(gitlabCom).toContainText("Connected")
  await expect(company).toContainText("No token")
  await expect(gitlab.getByText("How to create a token")).toHaveCount(1)
  await expect(dialog).toBeHidden()
  await expect(page.getByText("glpat-typed")).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  // The picture waits for the toast to go: it would cover the card's foot.
  if (process.env.UI_CHECK) {
    await expect(page.getByText("The token of GitLab was saved")).toBeHidden({ timeout: 15_000 })
  }
  await top(gitlab)
  await shot(page, "git-hosts-one-token")
  expect(calls).toEqual(["PUT /hosts/gitlab.com/token"])
})

// Scenario: the owner adds a company's server from the GitLab card; one the server refuses says why in the
// dialog. The added server is a row of the GitLab card. Then the owner removes it after being asked.
test("the owner adds and removes a company's git server", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  await card(page, "GitLab").getByRole("button", { name: "Add server" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await expect(dialog.getByRole("heading", { name: "Add a GitLab server" })).toBeVisible()
  await dialog.getByLabel("Address").fill("https://refused.example/team/app")
  await shot(page, "integrations-add-server")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(dialog.getByRole("alert")).toHaveText("the vault has no key")
  await shot(page, "integrations-add-server-refused")

  await dialog.getByLabel("Address").fill("gitlab.other.example")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  const added = card(page, "GitLab").getByLabel("GitLab host gitlab.other.example")
  await expect(added).toContainText("No token")

  await added.getByRole("button", { name: "Remove gitlab.other.example" }).click()
  await shot(page, "integrations-remove-confirm")
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove host" }).click()
  await expect(added).toHaveCount(0)

  expect(calls).toEqual([
    "PUT /hosts/refused.example",
    "PUT /hosts/gitlab.other.example",
    "DELETE /hosts/gitlab.other.example",
  ])
})

// Scenario: no repository is registered. The owner opens the Repositories page (it has no entry in the menu yet), registers one from its URL under the
// proposed name, and removes it.
test("the owner registers and removes a repository", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/repositories")

  await expect(page.getByText("No repositories yet")).toBeVisible()
  await shot(page, "repositories-empty")

  await page.getByRole("button", { name: "Add repository" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Repository").fill("https://git.company.example/team/app.git")
  await expect(dialog.getByLabel("Name")).toHaveValue("app")
  await dialog.getByLabel("Base branch").fill("develop")
  await shot(page, "repositories-add")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()

  await expect(page.locator("[data-slot=item]")).toContainText(
    "git.company.example/team/app · starts from develop",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "repositories-one")

  await page.getByRole("button", { name: "Remove app" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove repository" }).click()
  await expect(page.getByText("No repositories yet")).toBeVisible()
  expect(calls).toEqual(["PUT /repos/app", "DELETE /repos/app"])
})

// Scenario: Google has one account. The owner adds his work account: names it, lets Gmail only be read, opens
// Google's page from the link and pastes back the address the browser ended on. Then he disconnects the first one.
test("the owner signs a second Google account in and disconnects the first", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  const google = card(page, "Google")
  await expect(google).toContainText("Connected")
  await expect(google.getByLabel("Google account personal")).toContainText("owner@example.com")
  expect(await fits(page)).toBe(true)

  await google.getByRole("button", { name: "Add account" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Name").fill("work")
  await choose(page, dialog.getByLabel("Gmail"), "Read only")
  await choose(page, dialog.getByLabel("Sheets"), "Not connected")
  await shot(page, "integrations-google-add")
  await dialog.getByRole("button", { name: "Continue" }).click()

  await expect(dialog.getByRole("link", { name: /Open Google's sign-in page/ })).toBeVisible()
  await dialog.getByLabel("Address the browser ended on").fill("http://localhost:1/?state=s&code=c")
  await shot(page, "integrations-google-paste")
  await dialog.getByRole("button", { name: "Connect" }).click()

  await expect(google.getByLabel("Google account work")).toContainText("Gmail · read only")
  await expect(dialog).toBeHidden()
  await shot(page, "integrations-google-two")

  await google.getByRole("button", { name: "Disconnect personal" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Disconnect" }).click()
  await expect(google.getByLabel("Google account personal")).toHaveCount(0)

  expect(calls).toEqual([
    "POST /integrations/google/sign-ins",
    "POST /integrations/google/sign-ins/signin-1",
    "DELETE /integrations/google/connections/personal",
  ])
})

// Scenario: the owner looks at what agents may do with his Google account. The line under the account says it in
// words; the sheet lists each action of each service with its answer. He forbids sending mail, and the line says
// so. Lark, which does not go through the service proxy, has no such line.
test("the owner sets what agents may do with an account", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  const may = page.getByRole("button", { name: "What agents may do with personal" })
  await expect(may).toContainText("4 on their own · ask before 4")
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-agents-may")

  await may.click()
  const sheet = page.locator("[data-slot=sheet-content]")
  await expect(sheet.getByRole("heading", { name: "Google · personal" })).toBeVisible()
  await expect(sheet.getByRole("region", { name: "Drive" })).toContainText("Share a file")
  await expect(
    sheet.getByRole("group", { name: "Read mail" }).getByRole("button", { name: "On its own" }),
  ).toHaveAttribute("aria-pressed", "true")
  await shot(page, "integrations-actions")

  await sheet
    .getByRole("group", { name: "Send mail" })
    .getByRole("button", { name: "Never" })
    .click()
  await expect(
    sheet.getByRole("group", { name: "Send mail" }).getByRole("button", { name: "Never" }),
  ).toHaveAttribute("aria-pressed", "true")
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-actions-changed")

  await page.keyboard.press("Escape")
  await expect(sheet).toBeHidden()
  await expect(may).toContainText("4 on their own · ask before 3 · never 1")
  await expect(page.getByRole("button", { name: /What agents may do/ })).toHaveCount(1)

  expect(calls).toEqual(["PUT /integrations/google/connections/personal/actions/gmail.send"])
})

// Scenario: one Lark organization, two apps (issue #311). The owner adds his own app as "work" and a reports app
// as "report", each with its App ID and App Secret as a Lark channel's are given, with no link to open and nothing
// to paste. Each shows under the app's own name with its id. He changes the app of "report", then disconnects
// "work" after being told its secret leaves the vault. On a phone nothing runs off the side.
test("the owner adds two Lark apps and disconnects one", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  const lark = card(page, "Lark")
  await expect(lark).toContainText("Not connected")
  await shot(page, "integrations-lark-ready")

  const dialog = page.locator("[data-slot=dialog-content]")
  for (const [label, id] of [
    ["work", "cli_work"],
    ["report", "cli_report"],
  ] as const) {
    await lark.getByRole("button", { name: "Add app" }).click()
    await dialog.getByLabel("Name").fill(label)
    await dialog.getByLabel("App ID").fill(id)
    await dialog.getByLabel("App Secret").fill(`${label}-secret`)
    if (label === "report") await shot(page, "integrations-lark-add")
    await dialog.getByRole("button", { name: "Connect" }).click()
    await expect(dialog).toBeHidden()
  }

  await expect(lark.getByLabel("Lark account work")).toContainText("Hypomnema")
  await expect(lark.getByLabel("Lark account work")).toContainText("cli_work")
  await expect(lark.getByLabel("Lark account report")).toContainText("Superset Report")
  await expect(lark).toContainText("Connected")
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-lark-connected")

  await lark.getByLabel("Lark account report").getByRole("button", { name: "Change app" }).click()
  await expect(dialog.getByLabel("App ID")).toHaveValue("cli_report")
  await expect(dialog.getByLabel("Name")).toHaveCount(0)
  await shot(page, "integrations-lark-change")
  await dialog.getByLabel("App Secret").fill("reset-secret")
  await dialog.getByRole("button", { name: "Connect" }).click()
  await expect(dialog).toBeHidden()

  await lark.getByRole("button", { name: "Disconnect work" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("removes its secret from the vault")
  await shot(page, "integrations-lark-disconnect")
  await confirm.getByRole("button", { name: "Disconnect" }).click()
  await expect(lark.getByLabel("Lark account work")).toHaveCount(0)

  expect(calls).toEqual([
    "PUT /integrations/lark/connections/work",
    "PUT /integrations/lark/connections/report",
    "PUT /integrations/lark/connections/report",
    "DELETE /integrations/lark/connections/work",
  ])
})

// Scenario: the owner connects his company's Superset: its address, a user name and a password, with no client to
// set up first. A password Superset refuses is said in the dialog, which keeps what was typed, and nothing is
// connected. With the right one the account shows its user name, its address, Superset's version and that the
// last sign-in worked, with what agents may do with it and the switch for chats. He disconnects it after being
// told what is removed. On a phone nothing runs off the side.
test("the owner connects his Superset account and disconnects it", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  const superset = card(page, "Superset")
  await expect(superset).toContainText("No account")
  await expect(superset).toContainText("Dashboards and charts, read as data")
  await superset.scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-superset-ready")

  await superset.getByRole("button", { name: "Add account" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Name", { exact: true }).fill("vmaps")
  await dialog.getByLabel("Address").fill("https://superset.example.com")
  await dialog.getByLabel("User name").fill("hypomnema-reader")
  await expect(dialog.getByLabel("Password")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Password").fill("a-wrong-password")
  await shot(page, "integrations-superset-connect")
  await dialog.getByRole("button", { name: "Connect" }).click()

  await expect(dialog.getByRole("alert")).toContainText(
    "Superset refused the user name and password",
  )
  await expect(dialog.getByLabel("Address")).toHaveValue("https://superset.example.com")
  await shot(page, "integrations-superset-refused")
  await expect(superset).toContainText("No account")

  await dialog.getByLabel("Password").fill("the-right-password")
  await dialog.getByRole("button", { name: "Connect" }).click()
  await expect(dialog).toBeHidden()

  const account = superset.getByLabel("Superset account vmaps")
  await expect(account).toContainText("hypomnema-reader")
  await expect(account).toContainText("https://superset.example.com")
  await expect(account).toContainText("Superset 6.1.0")
  await expect(account).toContainText("The last sign-in worked")
  await expect(superset).toContainText("Connected")
  await expect(page.getByText("the-right-password")).toHaveCount(0)
  await expect(page.getByRole("button", { name: "What agents may do with vmaps" })).toContainText(
    "2 on their own · ask before 3",
  )
  await superset.scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-superset-connected")

  await account.getByRole("switch", { name: "Use in chats" }).click()
  await expect(account.getByRole("switch", { name: "Use in chats" })).toBeChecked()

  await superset.getByRole("button", { name: "Disconnect vmaps" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("removes its password from the vault")
  await confirm.getByRole("button", { name: "Disconnect" }).click()
  await expect(superset.getByLabel("Superset account vmaps")).toHaveCount(0)

  expect(calls).toEqual([
    "PUT /integrations/superset/connections/vmaps",
    "PUT /integrations/superset/connections/vmaps",
    "PATCH /integrations/superset/connections/vmaps",
    "DELETE /integrations/superset/connections/vmaps",
  ])
})

// Scenario: the owner adds the roles agents use in his AWS account (issue #331). The first role gives its key; a
// secret AWS refuses is said in the dialog and nothing is connected. The right one connects the role, which shows
// its ARN, its account and its key. A second role is added with the key's name alone. He disconnects one after
// being told its key stays while another role uses it. On a phone nothing runs off the side.
test("the owner adds AWS roles with one key", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  const aws = card(page, "AWS")
  await expect(aws).toContainText("Not connected")
  await expect(aws).toContainText("Roles of your AWS accounts")

  await aws.getByRole("button", { name: "Add role" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Name", { exact: true }).fill("main-read")
  await dialog
    .getByLabel("Role ARN")
    .fill("arn:aws:iam::123456789012:role/hypomnema/hypomnema-read")
  await expect(dialog.getByLabel("Key", { exact: true })).toHaveValue("main")
  await dialog.getByLabel("Access key ID").fill("AKIAEXAMPLEKEY00001")
  await expect(dialog.getByLabel("Secret access key")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Secret access key").fill("a-wrong-secret")
  await shot(page, "integrations-aws-add")
  await dialog.getByRole("button", { name: "Connect" }).click()

  await expect(dialog.getByRole("alert")).toContainText("AccessDenied")
  await shot(page, "integrations-aws-refused")

  await dialog.getByLabel("Secret access key").fill("the-right-secret")
  await dialog.getByRole("button", { name: "Connect" }).click()
  await expect(dialog).toBeHidden()

  const read = aws.getByLabel("AWS account main-read")
  await expect(read).toContainText("123456789012")
  await expect(read).toContainText("Role hypomnema-read")
  await expect(
    read.getByTitle("arn:aws:iam::123456789012:role/hypomnema/hypomnema-read"),
  ).toBeVisible()
  await expect(read).toContainText("Key main")
  await expect(page.getByText("the-right-secret")).toHaveCount(0)

  await aws.getByRole("button", { name: "Add role" }).click()
  await dialog.getByLabel("Name", { exact: true }).fill("main-machines")
  await dialog
    .getByLabel("Role ARN")
    .fill("arn:aws:iam::123456789012:role/hypomnema/hypomnema-machines")
  await expect(dialog).toContainText("Kept: main")
  await dialog.getByRole("button", { name: "Connect" }).click()
  await expect(dialog).toBeHidden()

  await expect(aws.getByLabel("AWS account main-machines")).toContainText("Key main")
  await expect(aws).toContainText("Connected")
  await aws.scrollIntoViewIfNeeded()
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-aws-connected")

  await aws.getByRole("button", { name: "Disconnect main-read" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText(
    "Its key is removed from the vault when no other role uses it",
  )
  await confirm.getByRole("button", { name: "Disconnect" }).click()
  await expect(aws.getByLabel("AWS account main-read")).toHaveCount(0)

  expect(calls.filter((c) => c.includes("/aws/"))).toEqual([
    "PUT /integrations/aws/connections/main-read",
    "PUT /integrations/aws/connections/main-read",
    "PUT /integrations/aws/connections/main-machines",
    "DELETE /integrations/aws/connections/main-read",
  ])
})
