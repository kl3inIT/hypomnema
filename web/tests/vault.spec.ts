import { expect, test, type Page } from "@playwright/test"
import { shot } from "./shot.js"
import type { Secret } from "../src/api/generated/types.gen.js"

// The Vault page, built, against a fake client API, on a phone and a desktop.

const now = new Date().toISOString()

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

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/me") return json({ id: "usr_1", name: "admin", kind: "owner" })

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

      const body = request.postDataJSON() as { value?: string; values?: Record<string, string> }
      const old = secrets.find((s) => s.name === name)
      const kept = secret(name, { ...old, version: (old?.version ?? 0) + 1 })
      values[name] = body.values ?? { value: body.value ?? "" }
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

const row = (page: Page, name: string) => page.getByRole("row", { name, exact: true })

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens the Vault from Settings. No value is on the page or asked for until he reveals a
// row; a token set shows each of its values; hiding takes the value off the page; the table's head reveals all.
test("the owner reveals and hides secrets' values", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/integrations")
  await page.getByRole("link", { name: "Vault" }).click()

  await expect(page.getByRole("heading", { name: "Vault" })).toBeVisible()
  await expect(row(page, "google-work")).toContainText("OAuth token set")
  await expect(page.getByText("ghp_")).toHaveCount(0)
  expect(calls).toEqual([])
  // The page itself does not scroll sideways: the table does, inside its box.
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
  await expect(page.getByRole("columnheader", { name: "Version" })).toBeVisible()
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

  await page.getByRole("button", { name: "Reveal values" }).click()
  await expect(page.getByText("sk-live-0123456789abcdef")).toBeVisible()
  await expect(page.getByText("the door code is 4812")).toBeVisible()
  await shot(page, "vault-all-revealed")
  await page.getByRole("button", { name: "Hide values" }).click()
  await expect(page.getByText("sk-live-0123456789abcdef")).toHaveCount(0)

  await page.getByRole("searchbox", { name: "Search secrets by name" }).fill("google")
  await expect(row(page, "openai-key")).toHaveCount(0)
  await expect(row(page, "google-work")).toBeVisible()
})

// Scenario: the owner adds a secret, edits it, is refused when deleting one in use, and deletes his own.
test("the owner adds, edits and deletes a secret", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/vault")

  await page.getByRole("button", { name: "Add secret" }).click()
  let dialog = page.locator("[data-slot=dialog-content]")
  await expect(dialog.getByLabel("Value")).toHaveAttribute("type", "password")
  await dialog.getByLabel("Name").fill("deploy-key")
  await dialog.getByLabel("Value").fill("first")
  await shot(page, "vault-add")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(row(page, "deploy-key")).toBeVisible()

  await row(page, "deploy-key").getByRole("button", { name: "Edit deploy-key" }).click()
  dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Value").fill("second")
  await dialog.getByRole("button", { name: "Save", exact: true }).click()
  await expect(dialog).toBeHidden()

  await row(page, "google-work").getByRole("button", { name: "Edit google-work" }).click()
  dialog = page.locator("[data-slot=dialog-content]")
  await expect(dialog).toContainText("complete replacement set")
  await dialog.getByLabel("refresh_token").fill("1//new")
  await shot(page, "vault-edit-token-set")
  await dialog.getByRole("button", { name: "Save", exact: true }).click()
  await expect(dialog).toBeHidden()

  await row(page, "openai-key").getByRole("button", { name: "Delete openai-key" }).click()
  dialog = page.getByRole("alertdialog")
  await dialog.getByRole("button", { name: "Delete secret" }).click()
  await expect(dialog.getByRole("alert")).toContainText("used by provider openai")
  await shot(page, "vault-delete-refused")
  await dialog.getByRole("button", { name: "Cancel" }).click()

  await row(page, "deploy-key").getByRole("button", { name: "Delete deploy-key" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete secret" }).click()
  await expect(row(page, "deploy-key")).toHaveCount(0)

  expect(calls).toEqual([
    "PUT /secrets/deploy-key",
    "PUT /secrets/deploy-key",
    "PUT /secrets/google-work",
    "DELETE /secrets/openai-key",
    "DELETE /secrets/deploy-key",
  ])
})

// Scenario: nothing is kept yet.
test("an empty vault says what it is for", async ({ page }) => {
  await fakeServer(page, [])
  await page.goto("/settings/vault")

  await expect(page.getByText("No secrets yet")).toBeVisible()
  await shot(page, "vault-empty")
})
