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

async function fakeServer(page: Page, start: { hosts?: Host[]; repos?: Repo[] } = {}) {
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
  let asked: { label: string; services: string[] } | undefined
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
              client_id: "1234.apps.example",
              services: ["gmail", "calendar", "drive", "docs", "sheets"],
              connections: accounts,
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
