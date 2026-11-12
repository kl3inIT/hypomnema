import { expect, test, type Page } from "@playwright/test"

// Settings, built, against a fake client API, on a phone and a desktop.

type Host = { host: string; kind: string; known: boolean; has_token: boolean }
type Repo = { name: string; host: string; path: string; base?: string; created_at: string }

const now = new Date().toISOString()

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}

async function fakeServer(
  page: Page,
  start: { hosts?: Host[]; repos?: Repo[]; larkApp?: boolean } = {},
) {
  let hosts = start.hosts ?? [
    { host: "github.com", kind: "github", known: true, has_token: true },
    { host: "gitlab.com", kind: "gitlab", known: true, has_token: false },
    { host: "git.company.example", kind: "gitlab", known: false, has_token: false },
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
  let apps: typeof accounts = []
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
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push(call)

    if (path === "/me") return json({ id: "usr_1", name: "admin", kind: "owner" })

    const [, kind, name, sub] = path.split("/")

    if (kind === "integrations" && path.endsWith("/actions")) {
      return json({ data: name === "google" ? actions : [] })
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
          h.host === name ? { ...h, has_token: request.method() === "PUT" } : h,
        )
      } else if (request.method() === "DELETE") {
        hosts = hosts.filter((h) => h.host !== name)
      } else if (!hosts.some((h) => h.host === name)) {
        const body = request.postDataJSON() as { kind: string }
        hosts = [...hosts, { host: name, kind: body.kind, known: false, has_token: false }]
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

    if (kind === "integrations" && sub === "connections" && request.method() === "PUT") {
      const added = {
        integration: name ?? "",
        label: path.split("/")[4] ?? "",
        account: "Hypomnema",
        services: [],
        sign_in_required: false,
        chats: false,
        created_at: now,
        updated_at: now,
      }
      apps = [...apps, added]

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
              client_id: start.larkApp === false ? undefined : "cli_a1b2c3d4e5f6a7b8",
              services: [],
              connections: apps,
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
test("the owner opens Settings and gives a host its token", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/")
  await page.getByRole("link", { name: "Settings" }).click()

  await expect(page).toHaveURL(/\/settings\/integrations$/)
  await expect(page.getByRole("heading", { name: "Integrations" })).toBeVisible()
  await expect(card(page, "GitHub")).toContainText("Connected")
  await expect(card(page, "GitLab")).toContainText("No token")
  await expect(card(page, "git.company.example")).toContainText("GitLab server")
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-list")

  await card(page, "GitLab").getByRole("button", { name: "Add token" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await expect(dialog.getByLabel("Access token")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Access token").fill("glpat-typed")
  await shot(page, "integrations-token-dialog")
  await dialog.getByRole("button", { name: "Save token" }).click()

  await expect(card(page, "GitLab")).toContainText("Connected")
  await expect(dialog).toBeHidden()
  await expect(page.getByText("glpat-typed")).toHaveCount(0)
  expect(calls).toEqual(["PUT /hosts/gitlab.com/token"])
})

// Scenario: the owner adds a company's server; one the server refuses says why in the dialog. Then removes the
// declared server after being asked.
test("the owner adds and removes a company's git server", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  await page.getByRole("button", { name: "Add a git server" }).click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Address").fill("https://refused.example/team/app")
  await shot(page, "integrations-add-server")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(dialog.getByRole("alert")).toHaveText("the vault has no key")
  await shot(page, "integrations-add-server-refused")

  await dialog.getByLabel("Address").fill("gitlab.other.example")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(card(page, "gitlab.other.example")).toContainText("No token")

  await card(page, "gitlab.other.example").getByRole("button", { name: "Remove" }).click()
  await shot(page, "integrations-remove-confirm")
  await page.getByRole("alertdialog").getByRole("button", { name: "Remove host" }).click()
  await expect(card(page, "gitlab.other.example")).toHaveCount(0)

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
  await dialog.getByLabel("Gmail").selectOption("Read only")
  await dialog.getByLabel("Sheets").selectOption("Not connected")
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
  const calls = await fakeServer(page, { larkApp: true })
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

// Scenario: the owner gave Hypomnema his Lark app's id and secret. He connects the app under a name, with no
// link to open and nothing to paste, sees it under the app's own name, and disconnects it after being told what
// stays. On a phone nothing runs off the side.
test("the owner connects his Lark app and disconnects it", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")

  const lark = card(page, "Lark")
  await expect(lark).toContainText("Not connected")
  await shot(page, "integrations-lark-ready")

  await lark.getByRole("button", { name: "Connect app" }).click()
  const dialog = page.getByRole("dialog")
  await dialog.getByLabel("Name").fill("work")
  await shot(page, "integrations-lark-connect")
  await dialog.getByRole("button", { name: "Connect" }).click()

  await expect(lark.getByLabel("Lark account work")).toContainText("Hypomnema")
  await expect(dialog).toBeHidden()
  await expect(lark).toContainText("Connected")
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-lark-connected")

  await lark.getByRole("button", { name: "Disconnect work" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("id and secret stay in the vault")
  await shot(page, "integrations-lark-disconnect")
  await confirm.getByRole("button", { name: "Disconnect" }).click()
  await expect(lark.getByLabel("Lark account work")).toHaveCount(0)

  expect(calls).toEqual([
    "PUT /integrations/lark/connections/work",
    "DELETE /integrations/lark/connections/work",
  ])
})

// Scenario: Lark's id and secret were not given yet: its card says how to give them and fits a phone.
test("a Lark app that is not set up says how to give its id and secret", async ({ page }) => {
  await fakeServer(page, { larkApp: false })
  await page.goto("/settings/integrations")

  const lark = card(page, "Lark")
  await expect(lark).toContainText("Not set up")
  await expect(lark).toContainText("integrations client set lark")
  expect(await fits(page)).toBe(true)
  await shot(page, "integrations-lark-unset")
})
