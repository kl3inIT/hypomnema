import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"
import type { GatewayKey as Key } from "../src/api/generated/types.gen.js"

// The Gateway keys page of Settings (issue #199, step 6), built, against a fake client API, on a phone and a
// desktop.

const now = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()

const served = (name: string, provider: string) => ({
  name,
  targets: [{ provider, upstream_model: name }],
  updated_at: iso(now),
})

const secret = "hyp_gateway_v1_Ab12Cd34ef56gh78ij90klmnopqrstuv_x9"

async function fakeServer(page: Page, start?: Key[]) {
  let keys: Key[] = start ?? [
    {
      id: "gwk_1",
      name: "opencode-laptop",
      prefix: "hyp_gateway_v1_Zx81Qa",
      models: ["*"],
      created_at: iso(now - 9 * 86_400_000),
      last_used_at: iso(now - 40 * 60_000),
    },
    {
      id: "gwk_2",
      name: "omp-desktop",
      prefix: "hyp_gateway_v1_Lm40Pr",
      models: ["gpt-5.5-codex", "claude-sonnet-5-5"],
      created_at: iso(now - 2 * 86_400_000),
    },
  ]
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (call === "POST /gateway/keys") {
      const body = request.postDataJSON() as { name: string; models?: string[] }
      calls.push(`${call} ${body.name} ${(body.models ?? []).join(",") || "every model"}`)

      const key: Key = {
        id: `gwk_${keys.length + 1}`,
        name: body.name,
        prefix: secret.slice(0, 21),
        models: body.models?.length ? body.models : ["*"],
        created_at: iso(now),
      }
      keys = [...keys, key]

      return json({ key, secret }, 201)
    }

    if (request.method() === "DELETE" && path.startsWith("/gateway/keys/")) {
      calls.push(call)
      keys = keys.filter((key) => `/gateway/keys/${key.id}` !== path)

      return route.fulfill({ status: 204 })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/keys":
        return json({ data: keys, live_worker_keys: 2 })
      case "GET /gateway/models":
        return json({
          data: [
            served("claude-opus-5-5", "claude"),
            served("claude-sonnet-5-5", "claude"),
            served("gpt-5.5", "codex"),
            served("gpt-5.5-codex", "codex"),
            served("gemini-3-pro", "antigravity"),
            served("deepseek-v4", "openrouter"),
          ],
        })
      case "GET /providers":
        return json({
          data: [
            { name: "claude", kind: "claude_subscription", models: [] },
            { name: "codex", kind: "codex_subscription", models: [] },
            { name: "antigravity", kind: "antigravity_subscription", models: [] },
            { name: "openrouter", kind: "openai_compatible", models: [] },
          ],
        })
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

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens the page. It gives the address a tool is pointed at, and the keys by name, start,
// models and when each was last used, never a value; the keys of workers are only counted.
test("the owner sees the gateway's address and keys", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/gateway-keys")

  await expect(page.getByLabel("Base URL", { exact: true })).toHaveValue(/\/v1$/)
  const laptop = page.getByRole("row", { name: "opencode-laptop" })
  await expect(laptop).toContainText("hyp_gateway_v1_Zx81Qa…")
  await expect(laptop).toContainText("Every model")
  await expect(laptop).toContainText("40m ago")
  const desktop = page.getByRole("row", { name: "omp-desktop" })
  // A model reads as its name, with its maker's mark.
  await expect(desktop).toContainText("Claude Sonnet 5.5")
  await expect(desktop).toContainText("GPT-5.5-Codex")
  await expect(desktop).toContainText("Never")
  await expect(page.getByTestId("worker-keys")).toContainText("2 more keys")
  expect(await fits(page)).toBe(true)
  await shot(page, "gateway-keys")
})

// Scenario: the owner makes a key for one tool, limited to some models, and copies it; its value is shown once and
// the list then shows it by its start. The models are grouped by the provider that serves them; a provider's box
// takes all of its models, and the search narrows the list. A key with no model chosen is refused before it is sent.
test("the owner creates a key and sees it once", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/gateway-keys")

  await page.getByRole("button", { name: "Create key" }).click()
  const dialog = page.getByRole("dialog", { name: /gateway key|Copy the key/ })
  await dialog.getByLabel("Name").fill("opencode-phone")
  await dialog.getByRole("radio", { name: /Only these models/ }).click()
  await dialog.getByRole("button", { name: "Create key" }).click()
  await expect(dialog).toContainText("Choose a model, or every model.")

  await expect(dialog.getByRole("group", { name: "Claude subscription" })).toContainText(
    "Claude Opus 5.5",
  )
  await expect(dialog.getByRole("group", { name: "openrouter" })).toContainText("deepseek-v4")
  await dialog.getByRole("checkbox", { name: "Every model of Claude subscription" }).click()
  await dialog.getByRole("checkbox", { name: /GPT-5.5-Codex/ }).click()
  await expect(dialog.getByTestId("chosen-count")).toContainText("3 selected")
  await expect(dialog.getByRole("checkbox", { name: /GPT-5.5-Codex/ })).toBeChecked()
  await dialog.getByLabel("Search models").focus()
  // The boxes fade to their colour; the picture waits for it.
  await page.waitForTimeout(300)
  await shot(page, "gateway-keys-create")

  await dialog.getByLabel("Search models").fill("gemini")
  await expect(dialog.getByRole("group", { name: "Antigravity" })).toBeVisible()
  await expect(dialog.getByRole("group", { name: "Claude subscription" })).toHaveCount(0)
  await dialog.getByLabel("Search models").fill("")
  await dialog.getByRole("button", { name: "Create key" }).click()

  await expect(dialog).toContainText("cannot be shown again")
  await expect(dialog.getByLabel("The new key")).toHaveValue(secret)
  await shot(page, "gateway-keys-created")
  await dialog.getByRole("button", { name: "Done" }).click()

  await expect(page.getByRole("row", { name: "opencode-phone" })).toContainText("Claude Opus 5.5")
  await expect(page.getByText(secret)).toHaveCount(0)
  expect(calls).toContain(
    "POST /gateway/keys opencode-phone claude-opus-5-5,claude-sonnet-5-5,gpt-5.5-codex",
  )
})

// Scenario: the owner revokes a key after being told a tool using it is refused from now on.
test("the owner revokes a key", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/gateway-keys")

  await page.getByRole("button", { name: "Revoke omp-desktop" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("refused from now on")
  await shot(page, "gateway-keys-revoke")
  await dialog.getByRole("button", { name: "Revoke key" }).click()

  await expect(page.getByRole("row", { name: "omp-desktop" })).toHaveCount(0)
  expect(calls).toContain("DELETE /gateway/keys/gwk_2")
})

// With no key yet, the page says what a key is for.
test("with no key the page says what to do", async ({ page }) => {
  await fakeServer(page, [])
  await page.goto("/settings/gateway-keys")

  await expect(page.getByText("No gateway keys yet")).toBeVisible()
  await shot(page, "gateway-keys-empty")
})
