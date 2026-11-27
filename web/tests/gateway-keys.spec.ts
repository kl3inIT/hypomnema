import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"
import type { GatewayKey as Key, GatewayKeyUpdate } from "../src/api/generated/types.gen.js"

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
const laptopSecret = "hyp_gateway_v1_Zx81Qa34ef56gh78ij90klmnopqrstuv_Pq4x"
const mask = (value: string) => value.slice(0, 6) + "•".repeat(value.length - 10) + value.slice(-4)

async function fakeServer(page: Page, start?: Key[]) {
  let keys: Key[] = start ?? [
    {
      id: "gwk_1",
      name: "opencode-laptop",
      prefix: "hyp_gateway_v1_Zx81Qa",
      masked_value: mask(laptopSecret),
      models: ["*"],
      endpoints: ["search", "fetch", "images", "audio"],
      created_at: iso(now - 9 * 86_400_000),
      last_used_at: iso(now - 40 * 60_000),
    },
    {
      id: "gwk_2",
      name: "omp-desktop",
      prefix: "hyp_gateway_v1_Lm40Pr",
      models: ["gpt-5.5-codex", "claude-sonnet-5-5"],
      endpoints: [],
      created_at: iso(now - 2 * 86_400_000),
    },
  ]
  const values = new Map([["gwk_1", laptopSecret]])
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (call === "POST /gateway/keys") {
      const body = request.postDataJSON() as {
        name: string
        models?: string[]
        endpoints?: Key["endpoints"]
      }
      calls.push(
        `${call} ${body.name} ${(body.models ?? []).join(",") || "every model"}; ${body.endpoints?.join(",") ?? "every kind"}`,
      )

      const key: Key = {
        id: `gwk_${keys.length + 1}`,
        name: body.name,
        prefix: secret.slice(0, 21),
        masked_value: mask(secret),
        models: body.models?.length ? body.models : ["*"],
        endpoints: body.endpoints ?? ["search", "fetch", "images", "audio"],
        created_at: iso(now),
      }
      keys = [...keys, key]
      values.set(key.id, secret)

      return json({ key, secret }, 201)
    }

    const id = path.split("/")[3] ?? ""
    if (
      request.method() === "GET" &&
      path.startsWith("/gateway/keys/") &&
      path.endsWith("/value")
    ) {
      calls.push(call)
      const value = values.get(id)
      return value ? json({ secret: value }) : json({ message: "No stored value" }, 404)
    }

    if (request.method() === "PATCH" && path.startsWith("/gateway/keys/")) {
      const body = request.postDataJSON() as GatewayKeyUpdate
      calls.push(`${call} ${body.name} ${body.models?.join(",")}; ${body.endpoints?.join(",")}`)
      const { endpoints, ...access } = body
      const kinds = endpoints?.filter((kind) => kind !== "*")
      keys = keys.map((key) =>
        key.id === id
          ? {
              ...key,
              ...access,
              endpoints: endpoints?.includes("*")
                ? ["search", "fetch", "images", "audio"]
                : (kinds ?? key.endpoints),
            }
          : key,
      )
      return json(keys.find((key) => key.id === id))
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
// models, what else each may use and when each was last used, masked by default; workers' keys are only counted.
test("the owner sees the gateway's address and keys", async ({ page, isMobile }) => {
  await fakeServer(page)
  await page.goto("/settings/gateway-keys")

  await expect(page.getByLabel("Base URL", { exact: true })).toHaveValue(/\/v1$/)
  const laptop = page.getByRole("row", { name: "opencode-laptop" })
  await expect(laptop).toContainText(mask(laptopSecret))
  await expect(laptop).not.toContainText(laptopSecret)
  await expect(laptop).toContainText("Every model")
  await expect(laptop).toContainText("Images")
  await laptop.getByRole("button", { name: "+1", exact: true }).click()
  const also = page.getByRole("dialog", { name: "Also" })
  await expect(also.getByRole("listitem")).toHaveText([
    "Web search",
    "Reading a web page",
    "Making and editing images",
    "Speech to text and text to speech",
  ])
  await page.keyboard.press("Escape")
  await expect(laptop).toContainText("40m ago")
  const desktop = page.getByRole("row", { name: "omp-desktop" })
  await expect(desktop).toContainText("hyp_gateway_v1_Lm40Pr…")
  const hidden = desktop.getByText("Can't be shown", { exact: true })
  await expect(hidden).toBeVisible()
  if (!isMobile) {
    await hidden.hover()
    await expect(
      page.getByText("Made before keys could be shown again; make a new one to copy it.", {
        exact: true,
      }),
    ).toBeVisible()
    await page.keyboard.press("Escape")
  }
  await expect(desktop.getByRole("button", { name: /Show key|Copy key/ })).toHaveCount(0)
  // A model reads as its name, with its maker's mark.
  await expect(desktop).toContainText("Claude Sonnet 5.5")
  await expect(desktop).toContainText("GPT-5.5-Codex")
  await expect(desktop).toContainText("Never")
  await expect(desktop).toContainText("Chat only")
  await expect(page.getByTestId("worker-keys")).toContainText("2 more keys")
  expect(await fits(page)).toBe(true)
  await shot(page, "gateway-keys")
})

test("a long model list shows three chips and opens the full list", async ({ page, isMobile }) => {
  const models = Array.from({ length: 15 }, (_, index) => `opencode-go/model-${index + 1}`)
  await fakeServer(page, [
    {
      id: "gwk_2",
      name: "omp-desktop",
      prefix: "hyp_gateway_v1_Lm40Pr",
      models,
      endpoints: ["search", "fetch", "images", "audio"],
      created_at: iso(now - 2 * 86_400_000),
    },
    {
      id: "gwk_1",
      name: "opencode-laptop",
      prefix: "hyp_gateway_v1_Zx81Qa",
      masked_value: mask(laptopSecret),
      models: ["*"],
      endpoints: ["search", "fetch", "images", "audio"],
      created_at: iso(now - 9 * 86_400_000),
    },
  ])
  await page.goto("/settings/gateway-keys")
  const row = page.getByRole("row", { name: "omp-desktop" })
  await expect(row).toBeVisible()
  await shot(page, "gateway-keys-long")
  const modelColumn = page.getByRole("columnheader", { name: "Models", exact: true })
  const index = await modelColumn.evaluate((header) =>
    Array.from(header.parentElement!.children).indexOf(header),
  )
  const cell = row.getByRole("cell").nth(index)
  if (process.env.UI_CHECK && isMobile) {
    await cell.evaluate((modelsCell) => {
      const table = modelsCell.closest("[data-slot=table-container]")!
      const nameCell = modelsCell.parentElement!.firstElementChild!
      table.scrollLeft +=
        modelsCell.getBoundingClientRect().left -
        table.getBoundingClientRect().left -
        nameCell.getBoundingClientRect().width
    })
    await shot(page, "gateway-keys-long-models")
  }
  for (const model of models.slice(0, 3)) {
    await expect(cell.getByText(model, { exact: true })).toBeVisible()
  }
  for (const model of models.slice(3)) {
    await expect(cell.getByText(model, { exact: true })).toHaveCount(0)
  }
  await cell.getByRole("button", { name: "+12", exact: true }).click()
  const list = page.getByRole("dialog", { name: "Models" }).getByRole("listitem")
  await expect(list).toHaveText(models)
  await expect(row.getByText("omp-desktop", { exact: true })).toBeInViewport()
  await shot(page, "gateway-keys-long-expanded")
})

// Scenario: the owner makes a key for one tool, limited to some models, and copies it; the list masks its value.
// The models are grouped by the provider that serves them; a provider's box
// takes all of its models, and the search narrows the list. A key with no model chosen is refused before it is sent.
// Besides chat a key may search, read pages, make images and hear or make speech (issue #694), each ticked at first; one unticked is not given
// (issue #664), and a key given every kind names none, so a kind added later is given too.
test("the owner creates a key and sees its value", async ({ page }) => {
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
  await expect(dialog.getByRole("checkbox", { name: /Web search/ })).toBeChecked()
  await expect(
    dialog.getByRole("checkbox", { name: /Speech to text and text to speech/ }),
  ).toBeChecked()
  await dialog.getByRole("checkbox", { name: /Making and editing images/ }).click()
  await expect(
    dialog.getByRole("checkbox", { name: /Making and editing images/ }),
  ).not.toBeChecked()
  await shot(page, "gateway-keys-create-endpoints")
  await dialog.getByRole("button", { name: "Create key" }).click()

  await expect(dialog).toContainText("You can copy it again from the list")
  await expect(dialog.getByLabel("The new key")).toHaveValue(secret)
  await shot(page, "gateway-keys-created")
  await dialog.getByRole("button", { name: "Done" }).click()

  const phone = page.getByRole("row", { name: "opencode-phone" })
  await expect(phone).toContainText(mask(secret))
  await expect(phone).toContainText("Claude Opus 5.5")
  await expect(phone).toContainText("Read pages")
  await expect(phone.getByText("Images", { exact: true })).toHaveCount(0)
  await expect(phone).toContainText("Audio")
  await expect(page.getByText(secret)).toHaveCount(0)
  expect(calls).toContain(
    "POST /gateway/keys opencode-phone claude-opus-5-5,claude-sonnet-5-5,gpt-5.5-codex; search,fetch,audio",
  )
})

test("the owner copies from the masked list, shows and hides, then edits the same key", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"])
  const calls = await fakeServer(page)
  await page.goto("/settings/gateway-keys")
  const laptop = page.getByRole("row", { name: "opencode-laptop" })

  await expect(laptop.getByText(mask(laptopSecret))).toBeVisible()
  expect(calls).not.toContain("GET /gateway/keys/gwk_1/value")
  await laptop.getByRole("button", { name: "Copy key for opencode-laptop" }).click()
  await expect
    .poll(() => page.evaluate<string>("navigator.clipboard.readText()"))
    .toBe(laptopSecret)
  await expect(laptop.getByText(laptopSecret, { exact: true })).toHaveCount(0)

  await laptop.getByRole("button", { name: "Show key for opencode-laptop" }).click()
  await expect(laptop.getByText(laptopSecret, { exact: true })).toBeVisible()
  await shot(page, "gateway-keys-shown")
  await laptop.getByRole("button", { name: "Hide key for opencode-laptop" }).click()
  await expect(laptop.getByText(laptopSecret, { exact: true })).toHaveCount(0)

  await laptop.getByRole("button", { name: "Edit opencode-laptop" }).click()
  const dialog = page.getByRole("dialog", { name: "Edit opencode-laptop" })
  await expect(dialog.getByLabel("Name")).toHaveValue("opencode-laptop")
  await expect(dialog.getByRole("radio", { name: /Every model/ })).toBeChecked()
  const images = dialog.getByRole("checkbox", { name: /Making and editing images/ })
  await expect(images).toBeChecked()
  await dialog.getByLabel("Name").fill("opencode-tool")
  await dialog.getByRole("radio", { name: /Only these models/ }).click()
  await dialog.getByRole("checkbox", { name: "GPT-5.5-Codex" }).click()
  await images.click()
  await shot(page, "gateway-keys-edit")
  await dialog.getByRole("button", { name: "Save changes" }).click()
  await expect(dialog).toHaveCount(0)

  const edited = page.getByRole("row", { name: "opencode-tool" })
  await expect(edited).toContainText("GPT-5.5-Codex")
  await expect(edited.getByText("Images", { exact: true })).toHaveCount(0)
  await expect(edited).toContainText(mask(laptopSecret))
  expect(calls).toContain(
    "PATCH /gateway/keys/gwk_1 opencode-tool gpt-5.5-codex; search,fetch,audio",
  )
  await edited.getByRole("button", { name: "Show key for opencode-tool" }).click()
  await expect(edited.getByText(laptopSecret, { exact: true })).toBeVisible()
  expect(await fits(page)).toBe(true)

  await edited.getByRole("button", { name: "Edit opencode-tool" }).click()
  const edit = page.getByRole("dialog", { name: "Edit opencode-tool" })
  await edit.getByRole("checkbox", { name: /Making and editing images/ }).click()
  await edit.getByRole("button", { name: "Save changes" }).click()
  await expect(edit).toHaveCount(0)
  await expect(edited.getByText("Images", { exact: true })).toBeVisible()
  expect(calls).toContain("PATCH /gateway/keys/gwk_1 opencode-tool gpt-5.5-codex; *")
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
