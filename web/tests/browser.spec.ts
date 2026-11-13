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
    url: "https://example.com/",
    title: "Example Domain",
    views: 0,
  },
  // A worker of another chat's Run: not offered in this chat.
  {
    id: "d-dsp_9",
    kind: "worker",
    dispatch_id: "dsp_9",
    run_id: "run_9",
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

// fakeServer answers the API and plays a tab on the WebSocket: it says the agents control, the tab's size and
// address, and sends one frame; asked for control it gives it. It keeps the tabs tickets were asked for, the
// WebSockets it was given and what the page sent.
async function fakeServer(page: Page, options: { noBrowser?: boolean; held?: boolean } = {}) {
  const asked: string[] = []
  const sockets: WebSocketRoute[] = []
  const sent: Record<string, unknown>[] = []
  const picture = await frame(page)

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

        return json({ installed: true, running: true, held: options.held ?? false, tabs })
      case "GET /runs":
        return json({
          data: [
            { id: "run_1", chat_id: "cht_1" },
            { id: "run_9", chat_id: "cht_9" },
          ],
        })
      case "GET /chats":
        return json({ data: [chat] })
      case "GET /chats/cht_1":
        return json(chat)
      case "GET /chats/cht_1/stream": {
        const said = {
          seq: 1,
          chat_id: chat.id,
          role: "owner",
          text: "add a health page",
          created_at: chat.created_at,
        }

        return route.fulfill({
          status: 200,
          contentType: "text/event-stream",
          body: `id: 1\nevent: message\ndata: ${JSON.stringify(said)}\n\n`,
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
      if (input.type === "takeControl")
        ws.send(JSON.stringify({ type: "control", controller: "you" }))
      if (input.type === "releaseControl")
        ws.send(JSON.stringify({ type: "control", controller: "agent" }))
      if (input.type === "navigate")
        ws.send(JSON.stringify({ type: "url", url: String(input.url) }))
    })
    ws.send(JSON.stringify({ type: "control", controller: "agent" }))
    ws.send(JSON.stringify({ type: "viewport", width: 1280, height: 720 }))
    ws.send(JSON.stringify({ type: "url", url: tabs[asked.at(-1) === "owner" ? 0 : 1].url }))
    ws.send(picture)
  })

  return { asked, sockets, sent, of: (type: string) => sent.filter((m) => m.type === type) }
}

const picture = (page: Page) => page.locator("canvas")
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
  await expect(page.getByRole("tab", { name: "health page" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await expect(page.getByRole("link", { name: "Browser" })).toHaveCount(0)
  // The chat's own tab is the top of the page, with its Bot's avatar: there is no header above it.
  await expect(page.getByRole("tab", { name: "health page" })).toHaveAttribute(
    "title",
    "Chat with coordinator",
  )
  await expect(page.getByRole("banner")).toHaveCount(0)

  // Only this chat's workers are offered.
  await page.getByRole("button", { name: "Open new" }).click()
  await expect(page.getByRole("menuitem")).toHaveText(["New chat", "My browser", "Example Domain"])
  await shot(page, "pane-open-new")
  await page.getByRole("menuitem", { name: "My browser" }).click()

  await expect(page.getByRole("tab", { name: "My browser" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
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
  await page.getByRole("tab", { name: "health page" }).click()
  await expect(page.getByText("add a health page")).toBeVisible()
  await page.getByRole("button", { name: "Open new" }).click()
  await page.getByRole("menuitem", { name: "Go to my browser" }).click()
  await expect(page.getByRole("tab", { name: "My browser" })).toHaveCount(1)

  await page.getByRole("button", { name: "Close My browser" }).click()
  await expect(page.getByRole("tab", { name: "My browser" })).toHaveCount(0)
  await expect(page.getByRole("tab", { name: "health page" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
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
  await expect(page.getByRole("tab", { name: /Example Domain/ })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await shot(page, "pane-browser-worker")

  await server.sockets[0].close({ code: 4404, reason: "no such tab" })
  await expect(page.getByRole("alert")).toContainText("This tab is closed.")
  await shot(page, "pane-browser-gone")
  expect(server.asked).toHaveLength(1)

  await page.getByRole("button", { name: "Reconnect" }).click()
  await expect.poll(() => server.asked).toEqual(["d-dsp_1", "d-dsp_1"])

  await page.reload()
  await expect(page.getByRole("tab", { name: /Example Domain/ })).toHaveAttribute(
    "aria-selected",
    "true",
  )
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
