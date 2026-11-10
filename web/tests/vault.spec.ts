import { expect, test, type Page } from "@playwright/test"

// The Vault page, built, against a fake client API, on a phone and a desktop.

const now = new Date().toISOString()

type Secret = {
  name: string
  kind: string
  version: number
  used_by: string[]
  key_missing: boolean
  login_required: boolean
  created_at: string
  updated_at: string
}

const secret = (name: string, m: Partial<Secret> = {}): Secret => ({
  name,
  kind: "generic",
  version: 1,
  used_by: [],
  key_missing: false,
  login_required: false,
  created_at: now,
  updated_at: now,
  ...m,
})

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}

async function fakeServer(page: Page, start?: Secret[]) {
  let secrets = start ?? [
    secret("conn-github-com-work", { used_by: ["connection github.com:work"], version: 2 }),
    secret("google-work", { kind: "oauth_token_set", used_by: ["connection google:work"] }),
    secret("openai-key", { used_by: ["provider openai"] }),
    secret("a-note-for-later"),
  ]
  const values: Record<string, Record<string, string>> = {
    "conn-github-com-work": { value: "ghp_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8" },
    "google-work": {
      refresh_token: "1//0gExampleRefreshTokenThatIsQuiteLongAndWrapsOnAPhone-abcdefghijklmnop",
      access_token: "ya29.a0ExampleAccessToken",
    },
    "openai-key": { value: "sk-live-0123456789abcdef" },
    "a-note-for-later": { value: "the door code is 4812" },
  }
  const calls: string[] = []

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const [, kind, name, sub] = path.split("/")

    if (kind === "secrets" && name) {
      calls.push(call)

      if (sub === "value") return json({ name, values: values[name] })

      if (request.method() === "DELETE") {
        const used = secrets.find((s) => s.name === name)?.used_by ?? []
        if (used.length > 0) {
          return json(
            {
              code: "conflict",
              message: `the secret is in use: "${name}" is used by ${used.join(", ")}`,
            },
            409,
          )
        }

        secrets = secrets.filter((s) => s.name !== name)

        return route.fulfill({ status: 204 })
      }

      const body = request.postDataJSON() as { value: string }
      const old = secrets.find((s) => s.name === name)
      const kept = secret(name, { ...old, version: (old?.version ?? 0) + 1 })
      values[name] = { value: body.value }
      secrets = [...secrets.filter((s) => s.name !== name), kept]

      return json(kept)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /secrets":
        return json({ data: secrets })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return calls
}

const row = (page: Page, name: string) => page.getByRole("listitem", { name })

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens the Vault from Settings. No value is on the page or asked for until he reveals a
// secret; a token set shows each of its values; hiding takes the value off the page.
test("the owner reveals and hides a secret's value", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")
  await page.getByRole("link", { name: "Vault" }).click()

  await expect(page.getByRole("heading", { name: "Vault" })).toBeVisible()
  await expect(row(page, "conn-github-com-work")).toContainText(
    "used by connection github.com:work",
  )
  await expect(row(page, "a-note-for-later")).toContainText("not used")
  await expect(page.getByText("ghp_")).toHaveCount(0)
  expect(calls).toEqual([])
  expect(await fits(page)).toBe(true)
  await shot(page, "vault-list")

  await row(page, "conn-github-com-work")
    .getByRole("button", { name: "Reveal conn-github-com-work" })
    .click()
  await expect(row(page, "conn-github-com-work").getByRole("status", { name: "value" })).toHaveText(
    "ghp_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8",
  )

  await row(page, "google-work").getByRole("button", { name: "Reveal google-work" }).click()
  await expect(
    row(page, "google-work").getByRole("status", { name: "refresh_token" }),
  ).toContainText("1//0gExample")
  expect(await fits(page)).toBe(true)
  await shot(page, "vault-revealed")

  await row(page, "conn-github-com-work")
    .getByRole("button", { name: "Hide conn-github-com-work" })
    .click()
  await expect(page.getByText("ghp_")).toHaveCount(0)
  expect(calls).toEqual([
    "GET /secrets/conn-github-com-work/value",
    "GET /secrets/google-work/value",
  ])
})

// Scenario: the owner adds a secret, replaces it, is refused when removing one in use, and removes his own.
test("the owner adds, replaces and removes a secret", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/vault")

  await page.getByRole("button", { name: "Add secret" }).click()
  let dialog = page.getByRole("dialog")
  await expect(dialog.getByLabel("Value")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Name").fill("deploy-key")
  await dialog.getByLabel("Value").fill("first")
  await shot(page, "vault-add")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(row(page, "deploy-key")).toContainText("version 1")

  await row(page, "deploy-key").getByRole("button", { name: "Replace deploy-key" }).click()
  dialog = page.getByRole("dialog")
  await dialog.getByLabel("Value").fill("second")
  await dialog.getByRole("button", { name: "Replace", exact: true }).click()
  await expect(row(page, "deploy-key")).toContainText("version 2")

  await row(page, "openai-key").getByRole("button", { name: "Remove openai-key" }).click()
  dialog = page.getByRole("dialog")
  await dialog.getByRole("button", { name: "Remove secret" }).click()
  await expect(dialog.getByRole("alert")).toContainText("the secret is in use")
  await shot(page, "vault-remove-refused")
  await dialog.getByRole("button", { name: "Cancel" }).click()

  await row(page, "deploy-key").getByRole("button", { name: "Remove deploy-key" }).click()
  await page.getByRole("dialog").getByRole("button", { name: "Remove secret" }).click()
  await expect(row(page, "deploy-key")).toHaveCount(0)

  expect(calls).toEqual([
    "PUT /secrets/deploy-key",
    "PUT /secrets/deploy-key",
    "DELETE /secrets/openai-key",
    "DELETE /secrets/deploy-key",
  ])
})

// Scenario: nothing is kept yet.
test("an empty vault says what it is for", async ({ page }) => {
  await fakeServer(page, [])
  await page.goto("/settings/vault")

  await expect(page.getByText("The vault is empty")).toBeVisible()
  await shot(page, "vault-empty")
})
