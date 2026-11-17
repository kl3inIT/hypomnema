import { expect, test, type Page, type WebSocketRoute } from "@playwright/test"

import { shot } from "./shot.js"

// The shared browser as a tab of a chat's pane, built, against a fake client API and a fake view WebSocket.

const chat = {
  id: "cht_1",
  title: "health page",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
}

// Another chat, whose Bot's tab is b-cht_9.
const other = { ...chat, id: "cht_9", title: "slow setup" }

const tabs = [
  {
    id: "owner",
    kind: "owner",
    url: "https://accounts.example.com/signin",
    title: "Sign in",
    views: 0,
  },
  {
    id: "d-dsp_1",
    kind: "worker",
    dispatch_id: "dsp_1",
    task_id: "tsk_1",
    run_id: "run_1",
    chat_id: "cht_1",
    url: "https://example.com/",
    title: "Example Domain",
    views: 0,
  },
  // The chat's Bot's own tab (issue #557).
  {
    id: "b-cht_1",
    kind: "coordinator",
    chat_id: "cht_1",
    bot_id: "coordinator",
    url: "https://maps.example/search",
    title: "Maps search",
    views: 0,
  },
  // A worker of another chat's Run, and another chat's Bot's tab: not offered in this chat.
  {
    id: "b-cht_9",
    kind: "coordinator",
    chat_id: "cht_9",
    bot_id: "coordinator",
    url: "https://elsewhere.example/",
    title: "Another chat's Bot's page",
    views: 0,
  },
  {
    id: "d-dsp_9",
    kind: "worker",
    dispatch_id: "dsp_9",
    run_id: "run_9",
    chat_id: "cht_9",
    url: "https://other.example/",
    title: "Another chat's page",
    views: 0,
  },
]

// frame is a JPEG the size of a tab, drawn by the browser itself: a white page with a dark bar and a form.
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
    c.fillStyle = "#18181b"
    c.fillRect(440, 384, 400, 44)

    return canvas.toDataURL("image/jpeg", 0.7).split(",")[1]
  })()`)

  return Buffer.from(data, "base64")
}

// fakeServer answers the API and plays a tab on the WebSocket: it says who controls, the tab's size and address,
// and sends one frame; asked for control it gives it. As the control plane, it keeps the owner's hold until the
// owner hands it back, not when a view closes (issue #603). It keeps the tabs tickets were asked for, the
// WebSockets it was given and what the page sent.
async function fakeServer(
  page: Page,
  options: { noBrowser?: boolean; held?: boolean; tabs?: typeof tabs; messages?: object[] } = {},
) {
  let listed = options.tabs ?? tabs
  const asked: string[] = []
  const sockets: WebSocketRoute[] = []
  const sent: Record<string, unknown>[] = []
  const picture = await frame(page)
  let held = options.held ?? false

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const view = /^POST \/browser\/tabs\/([^/]+)\/views$/.exec(call)
    if (view) {
      asked.push(view[1])

      return json({ ticket: `ticket-${asked.length}`, expires_at: new Date().toISOString() }, 201)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /browser":
        if (options.noBrowser) {
          return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
        }

        return json({ installed: true, running: true, held, tabs: listed })
      case "GET /bots":
        return json({ data: [{ id: "coordinator", name: "Atlas", main: true }] })
      case "GET /chats":
        return json({ data: [chat] })
      case "GET /chats/cht_1":
        return json(chat)
      case "GET /chats/cht_9":
        return json(other)
      case "GET /chats/cht_9/stream":
        return route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: `id: 1\nevent: message\ndata: ${JSON.stringify({ seq: 1, chat_id: other.id, role: "owner", text: "speed up the setup", created_at: other.created_at })}\n\n`,
        })
      case "GET /chats/cht_1/stream": {
        const said = {
          seq: 1,
          chat_id: chat.id,
          role: "owner",
          text: "add a health page",
          created_at: chat.created_at,
        }

        const all = [said, ...(options.messages ?? [])]

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
    sockets.push(ws)
    ws.onMessage((message) => {
      const input = JSON.parse(String(message)) as Record<string, unknown>
      sent.push(input)
      if (input.type === "takeControl") {
        held = true
        ws.send(JSON.stringify({ type: "control", controller: "you" }))
      }
      if (input.type === "releaseControl") {
        held = false
        ws.send(JSON.stringify({ type: "control", controller: "agent" }))
      }
      if (input.type === "navigate")
        ws.send(JSON.stringify({ type: "url", url: String(input.url) }))
    })
    ws.send(JSON.stringify({ type: "control", controller: held ? "you" : "agent" }))
    ws.send(JSON.stringify({ type: "viewport", width: 1280, height: 720 }))
    ws.send(JSON.stringify({ type: "url", url: tabs.find((t) => t.id === asked.at(-1))!.url }))
    ws.send(picture)
  })

  return {
    asked,
    sockets,
    sent,
    of: (type: string) => sent.filter((m) => m.type === type),
    // setTabs changes the tabs the browser lists from its next read on.
    setTabs: (next: typeof tabs) => {
      listed = next
    },
  }
}

const picture = (page: Page) => page.locator("canvas")
// paneTab is a tab of a pane's own strip; strip is the strip of the browser's tabs over a browser tab (issue #703).
const paneTab = (page: Page, name: string | RegExp) =>
  page.getByRole("tablist", { name: "Tabs of this pane" }).getByRole("tab", { name })
const strip = (page: Page) => page.getByRole("tablist", { name: "Tabs of the browser" })
const address = (page: Page) => page.getByRole("textbox", { name: "Address" })
const painted = (page: Page) =>
  expect.poll(() => page.evaluate<number>("document.querySelector('canvas').width")).toBe(1280)

// open opens something from the "+" of the chat's pane.
async function open(page: Page, item: string) {
  await page.getByRole("button", { name: "Open new" }).click()
  await page.getByRole("menuitem", { name: item }).click()
}

// Scenario: the owner signs in to a site for the agents, beside the chat. "+" opens the owner's own browser tab
// in the chat's pane; the owner only watches until taking control, and then a click, a key and an address reach
// the tab. The chat is a tab away, and the browser tab closes like any tab.
test("the owner opens the browser beside a chat, takes control, and uses the page", async ({
  page,
}) => {
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await expect(page.getByText("add a health page")).toBeVisible()
  await expect(paneTab(page, "health page")).toHaveAttribute("aria-selected", "true")
  await expect(page.getByRole("link", { name: "Browser" })).toHaveCount(0)
  // The chat's own tab is the top of the page, with its Bot's avatar: there is no header above it.
  await expect(paneTab(page, "health page")).toHaveAttribute("title", "Chat with Atlas")
  await expect(page.getByRole("banner")).toHaveCount(0)

  // Only this chat's tabs are offered, its Bot's and its worker's.
  await page.getByRole("button", { name: "Open new" }).click()
  await expect(page.getByRole("menuitem")).toHaveText([
    "New chat",
    "My browser",
    "Example Domainworker",
    "Maps searchAtlas",
  ])
  await shot(page, "pane-open-new")
  await page.getByRole("menuitem", { name: "My browser" }).click()

  await expect(paneTab(page, "My browser")).toHaveAttribute("aria-selected", "true")
  await painted(page)
  expect(server.asked).toEqual(["owner"])
  await expect.poll(() => server.of("ack")).toHaveLength(1)
  await expect(address(page)).toHaveValue("https://accounts.example.com/signin")
  await shot(page, "pane-browser-watching")

  // The tab fills the pane (issue #193): the pane's size goes up while only watching, and again when it changes.
  const pane = async () => {
    const rect = (await picture(page).locator("..").boundingBox())!
    return { type: "resize", width: Math.round(rect.width), height: Math.round(rect.height) }
  }
  await expect.poll(() => server.of("resize").at(-1)).toEqual(await pane())
  await page.setViewportSize({ width: 900, height: 700 })
  await expect.poll(async () => server.of("resize").at(-1)).toEqual(await pane())
  await page.setViewportSize(test.info().project.use.viewport!)

  // Watching: the page takes nothing.
  await expect(address(page)).toBeDisabled()
  await picture(page).click({ force: true })
  expect(server.of("mouse")).toHaveLength(0)

  await page.getByRole("button", { name: /Take control/ }).click()
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()

  // A click in the middle of the picture is the middle of the tab, whatever the pane's size.
  const box = (await picture(page).boundingBox())!
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
  await expect.poll(() => server.of("mouse").map((m) => m.action)).toEqual(["move", "down", "up"])

  const down = server.of("mouse")[1]
  expect(down).toMatchObject({ button: "left", clickCount: 1 })
  expect(down.x as number).toBeCloseTo(640, 0)
  expect(down.y as number).toBeCloseTo(360, 0)

  await page.keyboard.type("a")
  await expect
    .poll(() => server.of("key"))
    .toEqual([
      { type: "key", action: "down", key: "a", code: "KeyA", keyCode: 65, text: "a", modifiers: 0 },
      { type: "key", action: "up", key: "a", code: "KeyA", keyCode: 65, modifiers: 0 },
    ])

  await address(page).fill("example.org")
  await address(page).press("Enter")
  await expect.poll(() => server.of("navigate")).toEqual([{ type: "navigate", url: "example.org" }])
  await expect(address(page)).toHaveValue("example.org")
  await shot(page, "pane-browser-control")

  await page.getByRole("button", { name: /Hand back/ }).click()
  await expect(page.getByRole("button", { name: /Take control/ })).toBeVisible()
  expect(server.of("releaseControl")).toHaveLength(1)

  // The chat is a tab away and is as it was; the browser's own tab is gone to, not opened twice.
  await paneTab(page, "health page").click()
  await expect(page.getByText("add a health page")).toBeVisible()
  await page.getByRole("button", { name: "Open new" }).click()
  await page.getByRole("menuitem", { name: "Go to my browser" }).click()
  await expect(paneTab(page, "My browser")).toHaveCount(1)

  await page.getByRole("button", { name: "Close My browser" }).click()
  await expect(paneTab(page, "My browser")).toHaveCount(0)
  await expect(paneTab(page, "health page")).toHaveAttribute("aria-selected", "true")
})

// Scenario: the owner looks at what a worker of this chat is doing, and the worker's tab closes. A tab that is
// gone is said to be, and nothing is tried again until the owner asks. What was open is still open after a reload.
test("a worker's browser tab is watched beside its chat, and a tab that closed says so", async ({
  page,
}) => {
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")

  await open(page, "Example Domain")
  await expect.poll(() => server.asked).toEqual(["d-dsp_1"])
  await expect(address(page)).toHaveValue("https://example.com/")
  await expect(paneTab(page, /Example Domain/)).toHaveAttribute("aria-selected", "true")
  await shot(page, "pane-browser-worker")

  await server.sockets[0].close({ code: 4404, reason: "no such tab" })
  await expect(page.getByRole("alert")).toContainText("This tab is closed.")
  await shot(page, "pane-browser-gone")
  expect(server.asked).toHaveLength(1)

  await page.getByRole("button", { name: "Reconnect" }).click()
  await expect.poll(() => server.asked).toEqual(["d-dsp_1", "d-dsp_1"])

  await page.reload()
  await expect(paneTab(page, /Example Domain/)).toHaveAttribute("aria-selected", "true")
})

// Scenario: the chat's Bot browses (issue #557). Its tab is offered in its chat's pane as the Bot's, and opens in
// the viewer like any other, where the owner can take control.
test("a Bot's browser tab is watched beside its chat, as the Bot's", async ({ page }) => {
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")

  await open(page, "Maps search")
  await expect.poll(() => server.asked).toEqual(["b-cht_1"])
  await painted(page)
  await expect(address(page)).toHaveValue("https://maps.example/search")
  const shown = paneTab(page, /Maps search/)
  await expect(shown).toHaveAttribute("aria-selected", "true")
  await expect(shown).toHaveAttribute("title", "Atlas's tab, in this chat.")
  await expect(shown).toContainText("Atlas")
  await shot(page, "pane-browser-bot")

  await page.getByRole("button", { name: /Take control/ }).click()
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  expect(server.of("takeControl")).toHaveLength(1)

  // Its worker's tab opens beside it in the same pane.
  await open(page, "Example Domain")
  await expect(paneTab(page, /Example Domain/)).toHaveAttribute("aria-selected", "true")
  await expect(page.getByRole("menu")).toHaveCount(0)
  await shot(page, "pane-browser-bot-and-worker")
})

// A connection that drops is tried again by itself, with a new ticket.
test("a dropped view comes back by itself", async ({ page }) => {
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await open(page, "My browser")
  await expect.poll(() => server.sockets).toHaveLength(1)

  await server.sockets[0].close({ code: 1006, reason: "" })
  await expect.poll(() => server.asked, { timeout: 10_000 }).toEqual(["owner", "owner"])
  await painted(page)
})

// Scenario: the owner holds the browser and goes to the chat's tab in the same pane, then back. The browser's view
// closes, but the page does not hand the browser back: only "Hand back" does, and the control plane keeps the hold
// meanwhile (issue #603). The sidebar still says the owner holds it, and the tab comes back held.
test("switching to another tab of the pane keeps the owner's hold on the browser", async ({
  page,
}, info) => {
  test.skip(info.project.name === "phone", "a phone has no sidebar")
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await open(page, "My browser")
  await expect.poll(() => server.sockets).toHaveLength(1)
  let closed = 0
  server.sockets[0].onClose(() => closed++)
  await page.getByRole("button", { name: /Take control/ }).click()
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  await expect(page.getByText("You hold the browser.")).toBeVisible({ timeout: 10_000 })

  await paneTab(page, "health page").click()
  await expect(page.getByText("add a health page")).toBeVisible()
  await expect.poll(() => closed).toBe(1)
  expect(server.of("releaseControl")).toHaveLength(0)
  await expect(page.getByText("You hold the browser.")).toBeVisible()

  await paneTab(page, "My browser").click()
  await expect.poll(() => server.sockets).toHaveLength(2)
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  await expect(page.getByText("You hold the browser.")).toBeVisible()
})

// A control plane with no browser offers none: "+" has only a new chat.
test("a control plane with no browser offers no browser", async ({ page }) => {
  await fakeServer(page, { noBrowser: true })
  await page.goto("/chats/cht_1")

  await expect(page.getByText("add a health page")).toBeVisible()
  await expect(page.getByRole("tab")).toHaveCount(1)
  await page.getByRole("button", { name: "Open new" }).click()
  await expect(page.getByRole("menuitem")).toHaveText(["New chat"])
})

// While the owner holds the browser the sidebar says so, on every page.
test("the sidebar says that the owner holds the browser", async ({ page }, info) => {
  test.skip(info.project.name === "phone", "a phone has no sidebar")
  await fakeServer(page, { held: true })
  await page.goto("/machines")

  await expect(page.getByText("You hold the browser.")).toBeVisible()
  await shot(page, "sidebar-browser-held")
})

// shots takes a picture in the light theme and one in the dark, as the theme menu sets them.
async function shots(page: Page, name: string) {
  const theme = (value: string) =>
    page.evaluate(`(() => {
      localStorage.setItem("theme", "${value}")
      window.dispatchEvent(new StorageEvent("storage", { key: "theme", newValue: "${value}", storageArea: localStorage }))
    })()`)
  await theme("light")
  await shot(page, name)
  await theme("dark")
  await shot(page, `${name}-dark`)
}

// Scenario: the owner asked the chat's Bot to open a page to sign in to (issue #703). The Bot's new tab shows
// beside the chat by itself, once: on a desktop in a pane to its right, on a phone as a tab of the pane. A strip
// over it lists the browser's tabs of the chat, each with whose it is, and switches between them. Closed, the tab
// stays closed.
test("a tab the chat's Bot opens shows beside the chat once, with the browser's tabs over it", async ({
  page,
}, info) => {
  const phone = info.project.name === "phone"
  const server = await fakeServer(page, { tabs: tabs.filter((t) => t.id !== "b-cht_1") })
  await page.goto("/chats/cht_1")
  await expect(page.getByText("add a health page")).toBeVisible()
  // The tabs there when the page opened stay where they are.
  await page.waitForTimeout(500)
  expect(server.asked).toEqual([])

  server.setTabs(tabs)
  const shown = paneTab(page, /Maps search/)
  await expect(shown).toHaveAttribute("aria-selected", "true", { timeout: 10_000 })
  await expect.poll(() => server.asked).toEqual(["b-cht_1"])
  await painted(page)
  if (phone) {
    // A phone has no room beside: the tab is the pane's, the chat a tab away.
    await expect(paneTab(page, "health page")).toHaveAttribute("aria-selected", "false")
  } else {
    // The chat stays in front in its pane, the Bot's tab in a pane to its right.
    await expect(page).toHaveURL(/\/grid$/)
    await expect(paneTab(page, "health page")).toHaveAttribute("aria-selected", "true")
    await expect(page.getByText("add a health page")).toBeVisible()
  }

  // The strip lists the owner's tab, and the chat's Bot's and worker's tabs; not another chat's.
  await expect(strip(page).getByRole("tab")).toHaveText([
    /^My browser\s*you$/,
    /^Example Domain\s*worker$/,
    /^Maps search\s*Atlas$/,
  ])
  await expect(strip(page).getByRole("tab", { name: /Maps search/ })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await shots(page, "browser-beside-chat")

  // A click on the strip shows the worker's tab in the place of the Bot's.
  await strip(page)
    .getByRole("tab", { name: /Example Domain/ })
    .click()
  await expect.poll(() => server.asked).toEqual(["b-cht_1", "d-dsp_1"])
  await expect(address(page)).toHaveValue("https://example.com/")
  await expect(paneTab(page, /Example Domain/)).toHaveAttribute("aria-selected", "true")
  await expect(paneTab(page, /Maps search/)).toHaveCount(0)
  await shots(page, "browser-strip-switched")

  // The owner's own tab, picked on the strip, takes the place too; the strip still lists the chat's tabs, and
  // the Bot's is picked again from it.
  await strip(page)
    .getByRole("tab", { name: /My browser/ })
    .click()
  await expect(paneTab(page, "My browser")).toHaveAttribute("aria-selected", "true")
  await expect(strip(page).getByRole("tab")).toHaveText([
    /^My browser\s*you$/,
    /^Example Domain\s*worker$/,
    /^Maps search\s*Atlas$/,
  ])
  await strip(page)
    .getByRole("tab", { name: /Example Domain/ })
    .click()
  await expect(paneTab(page, /Example Domain/)).toHaveAttribute("aria-selected", "true")

  // Closed, it is not opened again while the browser keeps the tab.
  await page.getByRole("button", { name: "Close Example Domain" }).click()
  await page.waitForTimeout(6_000)
  await expect(paneTab(page, /Example Domain|Maps search/)).toHaveCount(0)
})

// Scenario: the chat's Bot and its worker each open a tab between two reads of the browser's list (issue #703).
// Both show beside the chat, or in its pane on a phone, once: neither is dropped, neither opens again.
test("the tabs a Bot and its worker open at once both show beside the chat", async ({
  page,
}, info) => {
  const phone = info.project.name === "phone"
  const server = await fakeServer(page, {
    tabs: tabs.filter((t) => t.id !== "b-cht_1" && t.id !== "d-dsp_1"),
  })
  await page.goto("/chats/cht_1")
  await expect(page.getByText("add a health page")).toBeVisible()
  await page.waitForTimeout(500)

  server.setTabs(tabs)
  await expect(paneTab(page, /Example Domain/)).toHaveCount(1, { timeout: 10_000 })
  await expect(paneTab(page, /Maps search/)).toHaveCount(1)
  if (!phone) await expect(page).toHaveURL(/\/grid$/)

  // A later read of the list keeps both, and opens neither again.
  await page.waitForTimeout(6_000)
  await expect(paneTab(page, /Example Domain/)).toHaveCount(1)
  await expect(paneTab(page, /Maps search/)).toHaveCount(1)
})

// Scenario: two chats are on the grid, one above the other, and both their Bots open a tab at once (issue #703).
// Each tab shows beside its own chat: the one opened first is not lost to the second.
test("the tabs two chats of the grid open at once both show beside their chats", async ({
  page,
}, info) => {
  test.skip(info.project.name === "phone", "a phone shows one chat: there is no grid")
  const server = await fakeServer(page, {
    tabs: tabs.filter((t) => t.id !== "b-cht_1" && t.id !== "b-cht_9"),
  })
  await page.addInitScript({
    content: `localStorage.setItem("hypomnema.grid", ${JSON.stringify(
      JSON.stringify({ axis: "rows", children: ["p1", "p2"] }),
    )})
    localStorage.setItem("hypomnema.panes", ${JSON.stringify(
      JSON.stringify({
        p1: { tabs: [{ kind: "chat", chat: "cht_1" }], active: "chat:cht_1" },
        p2: { tabs: [{ kind: "chat", chat: "cht_9" }], active: "chat:cht_9" },
      }),
    )})`,
  })
  await page.goto("/grid")
  await expect(page.getByText("add a health page")).toBeVisible()
  await expect(page.getByText("speed up the setup")).toBeVisible()
  await page.waitForTimeout(500)

  server.setTabs(tabs)
  await expect(paneTab(page, /Maps search/)).toHaveAttribute("aria-selected", "true", {
    timeout: 10_000,
  })
  await expect(paneTab(page, /Another chat's Bot's page/)).toHaveAttribute("aria-selected", "true")
  // A later read of the tabs keeps them both.
  await page.waitForTimeout(6_000)
  await expect(paneTab(page, /Maps search/)).toHaveCount(1)
  await expect(paneTab(page, /Another chat's Bot's page/)).toHaveCount(1)
  await expect(page.getByRole("main", { name: "Grid of chats" }).getByRole("region")).toHaveCount(4)
})

// Scenario: the Bot opened a page with agent_browser_open (issue #703). The call shows in the chat as a card with
// the page's title and address; "Open the tab" opens the Bot's tab beside the chat, or in the pane on a phone.
test("the Bot's call that opened a page is a card in the chat that opens its tab", async ({
  page,
}, info) => {
  const phone = info.project.name === "phone"
  const server = await fakeServer(page, {
    messages: [
      {
        role: "tool",
        text: "pi ran agent_browser_open",
        parts: [
          {
            type: "tool_result",
            id: "t1",
            name: "agent_browser_open",
            args: { url: "https://maps.example/search", headed: true },
            output: "https://maps.example/search",
            duration_ms: 1200,
          },
        ],
      },
      { role: "coordinator", text: "The page is open: sign in there, then tell me." },
    ],
  })
  await page.goto("/chats/cht_1")

  const card = page.getByRole("region", { name: "Browser tab: Maps search" })
  await expect(card).toBeVisible()
  await expect(card).toContainText("https://maps.example/search")
  await shots(page, "browser-open-card")

  await card.getByRole("button", { name: "Open the tab" }).click()
  await expect(paneTab(page, /Maps search/)).toHaveAttribute("aria-selected", "true")
  await expect.poll(() => server.asked).toEqual(["b-cht_1"])
  if (!phone) await expect(page).toHaveURL(/\/grid$/)
})
