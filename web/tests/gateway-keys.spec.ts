import { expect, test, type Locator, type Page } from "@playwright/test"

import { shot } from "./shot.js"
import type {
  GatewayKey as Key,
  GatewayKeyAccount,
  GatewayKeyUpdate,
  NewGatewayKey,
} from "../src/api/generated/types.gen.js"

// The Gateway keys page of Settings (issue #199, step 6), built, against a fake client API, on a phone and a
// desktop. The key's form is a sheet (issue #1277). UI_THEME picks the theme of the pictures UI_CHECK keeps.

test.beforeEach(async ({ page }) => {
  const theme = process.env.UI_THEME
  if (theme) await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
})

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

// The subscription accounts signed in: two Claude accounts and one Codex account.
const accounts = [
  { id: "acc_1", runtime: "claude", label: "work", status: "active", position: 1 },
  { id: "acc_2", runtime: "claude", label: "home", status: "active", position: 2 },
  { id: "acc_3", runtime: "codex", label: "chatgpt", status: "active", position: 1 },
]

const accountsOf = (ids?: string[]): GatewayKeyAccount[] | undefined =>
  ids && !ids.includes("*")
    ? ids.map((id) => ({ id, runtime: accounts.find((a) => a.id === id)?.runtime ?? "" }))
    : undefined

async function fakeServer(page: Page, start?: Key[]) {
  let keys: Key[] = start ?? [
    {
      id: "gwk_1",
      name: "opencode-laptop",
      prefix: "hyp_gateway_v1_Zx81Qa",
      masked_value: mask(laptopSecret),
      models: ["*"],
      endpoints: ["search", "fetch", "images", "audio"],
      search_order: [],
      created_at: iso(now - 9 * 86_400_000),
      last_used_at: iso(now - 40 * 60_000),
    },
    {
      id: "gwk_2",
      name: "omp-desktop",
      prefix: "hyp_gateway_v1_Lm40Pr",
      models: ["gpt-5.5-codex", "claude-sonnet-5-5"],
      endpoints: [],
      search_order: [],
      created_at: iso(now - 2 * 86_400_000),
    },
    {
      id: "gwk_3",
      name: "beyondpilot",
      prefix: "hyp_gateway_v1_Bp77Qa",
      models: ["*"],
      endpoints: ["search", "fetch"],
      accounts: [
        { id: "acc_1", runtime: "claude" },
        { id: "acc_2", runtime: "claude" },
      ],
      search_order: ["codex", "claude", "exa"],
      created_at: iso(now - 86_400_000),
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
      const body = request.postDataJSON() as NewGatewayKey
      calls.push(
        `${call} ${body.name} ${(body.models ?? []).join(",") || "every model"}; ${body.endpoints?.join(",") ?? "every kind"}; ${body.accounts?.join(",") ?? "every account"}; ${body.search_order?.join(",") ?? "like Settings"}`,
      )

      const key: Key = {
        id: `gwk_${keys.length + 1}`,
        name: body.name,
        prefix: secret.slice(0, 21),
        masked_value: mask(secret),
        models: body.models?.length ? body.models : ["*"],
        endpoints: body.endpoints ?? ["search", "fetch", "images", "audio"],
        accounts: accountsOf(body.accounts),
        search_order: body.search_order ?? [],
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
      calls.push(
        `${call} ${body.name} ${body.models?.join(",")}; ${body.endpoints?.join(",")}; ${body.accounts?.join(",")}; ${body.search_order?.join(",")}`,
      )
      const { endpoints, accounts: chosen, search_order, ...access } = body
      const kinds = endpoints?.filter((kind) => kind !== "*")
      keys = keys.map((key) =>
        key.id === id
          ? {
              ...key,
              ...access,
              endpoints: endpoints?.includes("*")
                ? ["search", "fetch", "images", "audio"]
                : (kinds ?? key.endpoints),
              accounts: chosen ? accountsOf(chosen) : key.accounts,
              search_order: search_order ?? key.search_order,
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
      case "GET /accounts":
        return json({ data: accounts })
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
  const hidden = desktop.getByRole("button", { name: "Can't be shown", exact: true })
  if (isMobile) {
    await hidden.tap()
  } else {
    await hidden.click()
  }
  await expect(
    page.getByText("Made before keys could be shown again; make a new one to copy it.", {
      exact: true,
    }),
  ).toBeVisible()
  await hidden.click()
  await expect(desktop.getByRole("button", { name: /Show key|Copy key/ })).toHaveCount(0)
  // A model reads as its name, with its maker's mark.
  await expect(desktop).toContainText("Claude Sonnet 5.5")
  await expect(desktop).toContainText("GPT-5.5-Codex")
  await expect(desktop).toContainText("Never")
  await expect(desktop).toContainText("Chat only")
  await expect(page.getByTestId("worker-keys")).toContainText("2 more keys")
  // A key with its own routing is marked under its name (issue #1277); the others are not.
  const routed = page.getByRole("row", { name: "beyondpilot" }).getByLabel("Own routing")
  await expect(routed).toHaveText(/2 Claude accounts\s*Own search order/)
  await expect(laptop.getByLabel("Own routing")).toHaveCount(0)
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
      search_order: [],
      created_at: iso(now - 2 * 86_400_000),
    },
    {
      id: "gwk_1",
      name: "opencode-laptop",
      prefix: "hyp_gateway_v1_Zx81Qa",
      masked_value: mask(laptopSecret),
      models: ["*"],
      endpoints: ["search", "fetch", "images", "audio"],
      search_order: [],
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

// pick chooses a model in the sheet's chip multiselect: searched, then picked from the list.
async function pick(sheet: Locator, query: string, name: RegExp) {
  const search = sheet.getByRole("combobox", { name: "Search models" })
  await search.fill(query)
  await sheet.page().getByRole("option", { name }).click()
  await search.fill("")
}

// Scenario (issue #1277): the owner makes a key for one tool in the sheet at the right side. The name comes first;
// Only these models opens a chip multiselect whose list is grouped by provider, and a key with no model chosen is
// refused before it is sent. Besides chat is one row of chips, each on at first; one turned off is not given (issue
// #664), and a key given every kind names none. Routing is closed, its header saying the key follows Settings; opened,
// the key takes one Claude account and its own search order, Codex, Claude, then Exa. Once made, the sheet shows the
// key once with what a tool needs, and the table marks the key's own routing.
test("the owner creates a key in the sheet and sees its value", async ({ page, isMobile }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/gateway-keys")
  if (isMobile) await page.keyboard.press("Escape")

  await page.getByRole("button", { name: "Create key" }).click()
  const sheet = page.getByRole("dialog", { name: /gateway key|Copy the key/ })
  await expect(sheet).toBeVisible()
  const box = await sheet.boundingBox()
  const width = page.viewportSize()!.width
  if (isMobile) {
    expect(box!.width).toBeGreaterThan(width - 2)
  } else {
    expect(box!.width).toBeGreaterThan(540)
    expect(box!.width).toBeLessThan(580)
  }
  await expect(sheet.getByTestId("routing-summary")).toHaveText("like Settings")
  await expect(sheet.getByRole("region", { name: "Accounts" })).toHaveCount(0)
  await shot(page, "gateway-keys-sheet-create")

  await sheet.getByLabel("Name").fill("opencode-phone")
  await sheet.getByRole("button", { name: "Only these", exact: true }).click()
  await sheet.getByRole("button", { name: "Create key" }).click()
  await expect(sheet).toContainText("Choose a model, or every model.")

  await sheet.getByRole("combobox", { name: "Search models" }).click()
  await expect(page.getByRole("group", { name: "Claude subscription" })).toContainText(
    "Claude Opus 5.5",
  )
  await expect(page.getByRole("group", { name: "openrouter" })).toContainText("deepseek-v4")
  await pick(sheet, "opus", /Claude Opus 5.5/)
  await pick(sheet, "sonnet", /Claude Sonnet 5.5/)
  await pick(sheet, "codex", /GPT-5.5-Codex/)
  await expect(sheet.getByTestId("chosen-count")).toContainText("3 of 6 chosen")
  await sheet.getByLabel("Name").click()

  const besides = sheet.getByRole("group", { name: "Besides chat" })
  await expect(besides.getByRole("button", { name: "Web search" })).toHaveAttribute(
    "aria-pressed",
    "true",
  )
  await besides.getByRole("button", { name: "Making and editing images" }).click()
  await expect(besides.getByRole("button", { name: "Making and editing images" })).toHaveAttribute(
    "aria-pressed",
    "false",
  )
  await expect(sheet).toContainText("/v1/web/fetch")
  await shot(page, "gateway-keys-sheet-models")

  await sheet.getByRole("button", { name: /^Routing/ }).click()
  const accountsChoice = sheet.getByRole("region", { name: "Accounts" })
  await accountsChoice.getByRole("button", { name: "Only these" }).click()
  await accountsChoice
    .getByRole("group", { name: "Claude accounts" })
    .getByRole("button", { name: "work" })
    .click()
  const order = sheet.getByRole("region", { name: "Web search order" })
  await order.getByRole("button", { name: "Its own" }).click()
  for (const provider of ["Codex", "Claude", "Exa"]) {
    await order.getByRole("button", { name: `Add ${provider}` }).click()
  }
  await expect(order.getByRole("list", { name: "Asked first" })).toHaveText(
    /1\s*Codex.*2\s*Claude.*3\s*Exa/,
  )
  await expect(sheet.getByTestId("routing-summary")).toHaveText("1 account · own search order")
  await order.scrollIntoViewIfNeeded()
  await shot(page, "gateway-keys-sheet-routing")
  await sheet.getByRole("button", { name: "Create key" }).click()

  await expect(sheet).toContainText("You can copy it again from the list")
  await expect(sheet.getByLabel("The new key")).toHaveValue(secret)
  const connect = sheet.getByLabel("Connect a tool")
  await expect(connect).toContainText("/v1")
  await expect(connect).toContainText(`OPENAI_API_KEY=${secret}`)
  await expect(connect).toContainText(`ANTHROPIC_AUTH_TOKEN=${secret}`)
  await shot(page, "gateway-keys-sheet-created")
  await sheet.getByRole("button", { name: "Done" }).click()

  const phone = page.getByRole("row", { name: "opencode-phone" })
  await expect(phone).toContainText(mask(secret))
  await expect(phone).toContainText("Claude Opus 5.5")
  await expect(phone).toContainText("Read pages")
  await expect(phone.getByText("Images", { exact: true })).toHaveCount(0)
  await expect(phone).toContainText("Audio")
  await expect(phone.getByLabel("Own routing")).toHaveText(/1 Claude account\s*Own search order/)
  await expect(page.getByText(secret)).toHaveCount(0)
  expect(calls).toContain(
    "POST /gateway/keys opencode-phone claude-opus-5-5,claude-sonnet-5-5,gpt-5.5-codex; search,fetch,audio; acc_1; codex,claude,exa",
  )
  await shot(page, "gateway-keys-badges")
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
  const sheet = page.getByRole("dialog", { name: "Edit opencode-laptop" })
  await expect(sheet.getByLabel("Name")).toHaveValue("opencode-laptop")
  await expect(sheet.getByRole("button", { name: "Every model" })).toHaveAttribute(
    "aria-pressed",
    "true",
  )
  const images = sheet.getByRole("button", { name: "Making and editing images" })
  await expect(images).toHaveAttribute("aria-pressed", "true")
  await sheet.getByLabel("Name").fill("opencode-tool")
  await sheet.getByRole("button", { name: "Only these", exact: true }).click()
  await pick(sheet, "codex", /GPT-5.5-Codex/)
  await sheet.getByLabel("Name").click()
  await images.click()
  await shot(page, "gateway-keys-sheet-edit")
  await sheet.getByRole("button", { name: "Save changes" }).click()
  await expect(sheet).toHaveCount(0)

  const edited = page.getByRole("row", { name: "opencode-tool" })
  await expect(edited).toContainText("GPT-5.5-Codex")
  await expect(edited.getByText("Images", { exact: true })).toHaveCount(0)
  await expect(edited).toContainText(mask(laptopSecret))
  expect(calls).toContain(
    "PATCH /gateway/keys/gwk_1 opencode-tool gpt-5.5-codex; search,fetch,audio; *; ",
  )
  await edited.getByRole("button", { name: "Show key for opencode-tool" }).click()
  await expect(edited.getByText(laptopSecret, { exact: true })).toBeVisible()
  expect(await fits(page)).toBe(true)

  await edited.getByRole("button", { name: "Edit opencode-tool" }).click()
  const edit = page.getByRole("dialog", { name: "Edit opencode-tool" })
  await edit.getByRole("button", { name: "Making and editing images" }).click()
  await edit.getByRole("button", { name: "Save changes" }).click()
  await expect(edit).toHaveCount(0)
  await expect(edited.getByText("Images", { exact: true })).toBeVisible()
  expect(calls).toContain("PATCH /gateway/keys/gwk_1 opencode-tool gpt-5.5-codex; *; *; ")
})

// Scenario (issue #1277): the owner opens a key with its own routing. The Routing header says so while closed; open,
// it shows the key's accounts and order, and the owner sets both back to Settings.
test("the owner sets a key's own routing back to Settings", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/gateway-keys")

  await page.getByRole("button", { name: "Edit beyondpilot" }).click()
  const sheet = page.getByRole("dialog", { name: "Edit beyondpilot" })
  await expect(sheet.getByTestId("routing-summary")).toHaveText("2 accounts · own search order")
  await shot(page, "gateway-keys-sheet-edit-routed")
  await sheet.getByRole("button", { name: /^Routing/ }).click()
  const accountsChoice = sheet.getByRole("region", { name: "Accounts" })
  const claude = accountsChoice.getByRole("group", { name: "Claude accounts" })
  await expect(claude.getByRole("button", { name: "work" })).toHaveAttribute("aria-pressed", "true")
  await expect(claude.getByRole("button", { name: "home" })).toHaveAttribute("aria-pressed", "true")
  const order = sheet.getByRole("region", { name: "Web search order" })
  await expect(order.getByRole("list", { name: "Asked first" })).toHaveText(
    /1\s*Codex.*2\s*Claude.*3\s*Exa/,
  )
  await order.scrollIntoViewIfNeeded()
  await shot(page, "gateway-keys-sheet-edit-routing")

  await accountsChoice.getByRole("button", { name: "Every account" }).click()
  await order.getByRole("button", { name: "Like Settings" }).click()
  await expect(sheet.getByTestId("routing-summary")).toHaveText("like Settings")
  await sheet.getByRole("button", { name: "Save changes" }).click()
  await expect(sheet).toHaveCount(0)

  await expect(
    page.getByRole("row", { name: "beyondpilot" }).getByLabel("Own routing"),
  ).toHaveCount(0)
  expect(calls).toContain("PATCH /gateway/keys/gwk_3 beyondpilot ; undefined; *; ")
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
