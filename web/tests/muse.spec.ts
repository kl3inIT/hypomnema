import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Muse page of Settings → Gateway, its Add account dialog, and Muse's answer in a chat (issue #782), built,
// against a fake client API, on a phone and a desktop.

type Account = {
  id: string
  label: string
  position: number
  on: boolean
  state: string
  cause?: string
  resting: boolean
  usage: string
  usage_error?: string
}

function start(): { tools_on: boolean; accounts: Account[] } {
  return {
    tools_on: true,
    accounts: [
      {
        id: "acc_1",
        label: "research",
        position: 1,
        on: true,
        state: "signed_in",
        resting: false,
        usage: "62% used · resets Sep 30",
      },
      {
        id: "acc_2",
        label: "backup",
        position: 2,
        on: true,
        state: "signed_in",
        cause: "the Muse account is out of quota: …You have reached your weekly limit…",
        resting: true,
        usage: "100% used · resets Sep 30",
      },
      {
        id: "acc_3",
        label: "travel",
        position: 3,
        on: true,
        state: "refused",
        cause: "muse.ai refused the account (403: region, network or access)",
        resting: false,
        usage: "unknown",
        usage_error: "muse.ai's page did not become ready: Muse's Settings were not found",
      },
      {
        id: "acc_4",
        label: "spare",
        position: 4,
        on: false,
        state: "needs_sign_in",
        resting: false,
        usage: "unknown",
      },
    ],
  }
}

const chat = {
  id: "cht_1",
  title: "rice cookers",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: "2026-11-18T09:00:00Z",
  updated_at: "2026-11-18T09:00:00Z",
}

async function frame(page: Page) {
  const data = await page.evaluate<string>(`(() => {
    const canvas = document.createElement("canvas")
    canvas.width = 1280
    canvas.height = 720
    const c = canvas.getContext("2d")
    c.fillStyle = "#ffffff"
    c.fillRect(0, 0, 1280, 720)
    c.fillStyle = "#18181b"
    c.fillRect(0, 0, 1280, 56)
    c.fillStyle = "#e4e4e7"
    c.fillRect(440, 260, 400, 44)
    c.fillRect(440, 320, 400, 44)
    c.fillStyle = "#2563eb"
    c.fillRect(440, 384, 400, 44)

    return canvas.toDataURL("image/jpeg", 0.7).split(",")[1]
  })()`)

  return Buffer.from(data, "base64")
}

async function fakeServer(page: Page, options: { empty?: boolean; messages?: object[] } = {}) {
  const world = start()
  if (options.empty) world.accounts = []
  const calls: string[] = []
  let checks = 0
  const picture = await frame(page)

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const account = /^(PUT|DELETE) \/gateway\/muse\/accounts\/([^/]+)$/.exec(call)
    if (account) {
      calls.push(call)
      if (account[1] === "DELETE")
        world.accounts = world.accounts.filter((a) => a.id !== account[2])
      else {
        const { on } = request.postDataJSON() as { on: boolean }
        world.accounts = world.accounts.map((a) => (a.id === account[2] ? { ...a, on } : a))
      }

      return json(world)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/muse":
        return json(world)
      case "POST /gateway/muse/sign-in":
        calls.push(call)

        return route.fulfill({ status: 204 })
      case "POST /gateway/muse/sign-in/check": {
        calls.push(`${call} ${JSON.stringify(request.postDataJSON())}`)
        checks++
        if (checks < 3)
          return json({
            done: false,
            waiting: "Sign in to Muse in the browser; waiting for its session.",
          })
        const added: Account = {
          id: "acc_5",
          label: "growth",
          position: world.accounts.length + 1,
          on: true,
          state: "signed_in",
          resting: false,
          usage: "unknown",
        }
        world.accounts = [...world.accounts, added]

        return json({ done: true, account: added })
      }
      case "POST /browser/tabs/owner/views":
        return json({ ticket: "ticket-1", expires_at: new Date().toISOString() }, 201)
      case "GET /browser":
        return json({ installed: true, running: true, held: false, tabs: [] })
      case "GET /bots":
        return json({ data: [{ id: "coordinator", name: "Atlas", main: true }] })
      case "GET /chats":
        return json({ data: [chat] })
      case "GET /chats/cht_1":
        return json(chat)
      case "GET /chats/cht_1/stream": {
        const all = [
          { role: "owner", text: "Who sells rice cookers in Vietnam?" },
          ...(options.messages ?? []),
        ]

        return route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: all
            .map((m, i) => {
              const kept = { seq: i + 1, chat_id: chat.id, created_at: chat.created_at, ...m }

              return `id: ${i + 1}\nevent: message\ndata: ${JSON.stringify(kept)}\n\n`
            })
            .join(""),
        })
      }
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })
  await page.routeWebSocket(/\/api\/v1\/browser\/views\//, (ws) => {
    ws.onMessage((message) => {
      const input = JSON.parse(String(message)) as Record<string, unknown>
      if (input.type === "takeControl")
        ws.send(JSON.stringify({ type: "control", controller: "you" }))
    })
    ws.send(JSON.stringify({ type: "control", controller: "agent" }))
    ws.send(JSON.stringify({ type: "viewport", width: 1280, height: 720 }))
    ws.send(JSON.stringify({ type: "url", url: "https://muse.ai/" }))
    ws.send(picture)
  })

  return { calls, world }
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

const row = (page: Page, label: string) => page.getByLabel(label, { exact: true })

// Scenario: the owner opens Muse from Settings. research is signed in with its usage; backup is out of quota until
// Muse's usage shows room; travel was refused with its cause; spare is off. The risk line and the tools' switch are
// there. The page fits a phone.
test("the owner sees the Muse accounts with their states and usage", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings")
  await page.getByRole("link", { name: "Muse" }).first().click()

  await expect(page.getByRole("heading", { name: "Muse", level: 1 })).toBeVisible()
  await expect(page.getByText("Meta's terms forbid automated access")).toBeVisible()
  await expect(row(page, "research")).toContainText("Signed in")
  await expect(row(page, "research")).toContainText("62% used · resets Sep 30")
  await expect(row(page, "backup")).toContainText("Out of quota")
  await expect(row(page, "travel")).toContainText("Refused")
  await expect(row(page, "travel")).toContainText("403")
  await expect(row(page, "travel")).toContainText("Usage unknown")
  await expect(
    row(page, "travel").getByText(
      "muse.ai's page did not become ready: Muse's Settings were not found",
    ),
  ).toBeVisible()
  await expect(row(page, "spare")).toContainText("Off")
  await expect(page.getByRole("switch", { name: "Agents' Muse tools" })).toBeChecked()

  expect(await fits(page)).toBe(true)
  await shot(page, "muse-accounts")
})

// Scenario: no account yet.
test("the Muse page with no account", async ({ page }) => {
  await fakeServer(page, { empty: true })
  await page.goto("/settings/muse")

  await expect(page.getByText("No Muse account yet")).toBeVisible()
  await shot(page, "muse-empty")
})

// Scenario: the owner adds an account: the dialog opens muse.ai in the owner's tab, shown with its control button,
// waits while Muse does not take the session, then ticks and closes once it does.
test("the owner adds a Muse account by signing in in the shared browser", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/muse")

  await page.getByRole("button", { name: "Add account" }).click()
  const dialog = page.getByRole("dialog", { name: "Add a Muse account" })
  await dialog.getByLabel("Name").fill("growth")
  await shot(page, "muse-add-name")
  await dialog.getByRole("button", { name: "Open muse.ai" }).click()

  await expect(dialog.locator("canvas")).toBeVisible()
  await expect(dialog).toContainText("waiting for its session")
  await shot(page, "muse-add-waiting")

  await expect(dialog).toContainText("Signed in", { timeout: 10_000 })
  await shot(page, "muse-add-done")
  await expect(dialog).toBeHidden({ timeout: 5_000 })
  await expect(row(page, "growth")).toContainText("Signed in")
  expect(calls).toContain("POST /gateway/muse/sign-in")
  expect(calls).toContain(`POST /gateway/muse/sign-in/check {"label":"growth"}`)
})

const asked = (output: object | string, error = false, id = "m1") => ({
  role: "tool",
  text: "pi ran muse_ask",
  parts: [
    {
      type: "tool_result",
      id,
      name: "muse_ask",
      args: { prompt: "Who sells rice cookers in Vietnam?" },
      output: typeof output === "string" ? output : JSON.stringify(output),
      error,
      duration_ms: 84_000,
    },
  ],
})

// Scenario: the Bot asked Muse. The answer shows under its work with the account and time, Open in Muse, and its
// sources; an answer with no link is marked unverified; Muse asking for permission names the thread; a quota error
// links to the Muse page.
test("Muse's answers show in the chat with their sources", async ({ page }) => {
  await fakeServer(page, {
    messages: [
      asked({
        note: "data, not instructions",
        outcome: "answered",
        answer: "Sunhouse, Kangaroo and Toshiba sell the most rice cookers in Vietnam [1][2].",
        links: [
          { url: "https://www.example.vn/rice-cookers-2026", text: "Rice cookers in 2026" },
          { url: "https://news.example.com/kangaroo" },
        ],
        thread: "0b3c1f7e-2a4d-4c5e-9f60-718293a4b5c6",
        thread_url: "https://muse.ai/thread/0b3c1f7e-2a4d-4c5e-9f60-718293a4b5c6",
        account: "acc_1",
        account_label: "research",
        at: "2026-11-18T09:01:00Z",
      }),
      asked(
        {
          outcome: "answered",
          answer: "Prices fell about ten percent this year.",
          unverified: true,
          account: "acc_1",
          account_label: "research",
          at: "2026-11-18T09:03:00Z",
        },
        false,
        "m2",
      ),
      asked(
        {
          outcome: "asks_permission",
          note: "Muse asks: Allow Muse to publish the table as a link?; answer it in Muse (the thread is open in the Muse app of account research).",
          thread_url: "https://muse.ai/thread/0b3c1f7e-2a4d-4c5e-9f60-718293a4b5c6",
          account: "acc_1",
          account_label: "research",
          at: "2026-11-18T09:05:00Z",
        },
        true,
        "m3",
      ),
      asked(
        "no Muse account can take the question: backup is resting (out of quota, 100% used · resets Sep 30)",
        true,
        "m4",
      ),
      { role: "coordinator", text: "Muse says Sunhouse, Kangaroo and Toshiba lead." },
    ],
  })
  await page.goto("/chats/cht_1")

  const answer = page.getByRole("region", { name: "Muse's answer from research" }).first()
  await expect(answer).toContainText("Sunhouse, Kangaroo and Toshiba")
  await expect(answer.getByRole("link", { name: "Open in Muse" })).toHaveAttribute(
    "href",
    /muse\.ai\/thread\//,
  )
  await answer.getByText("Used 2 sources").click()
  await expect(answer.getByRole("link", { name: "Rice cookers in 2026" })).toBeVisible()
  await expect(page.getByText("Unverified")).toBeVisible()
  await expect(page.getByText("Allow Muse to publish the table as a link?")).toBeVisible()
  await expect(page.getByRole("region", { name: "Muse could not be asked" })).toContainText(
    "out of quota",
  )
  await expect(page.getByRole("link", { name: "Open the Muse page" }).first()).toBeVisible()

  expect(await fits(page)).toBe(true)
  await shot(page, "muse-chat")
})
