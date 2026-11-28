import { expect, test, type Page, type WebSocketRoute } from "@playwright/test"

import { shot } from "./shot.js"

// The Android phone as a tab of a chat's pane (issue #417, step 3), built, against a fake client API and a fake view
// WebSocket that plays a real H.264 stream, encoded by the browser itself.

const chat = {
  id: "cht_1",
  title: "sign in to the shop app",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: "2026-11-15T10:00:00Z",
  updated_at: "2026-11-15T10:00:00Z",
}

// Another Bot's main chat, which its row in the sidebar carries to the grid (issue #471).
const other = { ...chat, id: "cht_2", title: "check the order page", bot: "reporter" }

const bot = (id: string, position: number, mainChat: string) => ({
  id,
  main: id === "coordinator",
  name: id,
  position,
  main_chat: mainChat,
  instructions: "",
  memory: "",
  memory_limit: 2200,
  fallbacks: [],
  skills: [],
  skills_off: [],
  tools: [],
  tools_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  scopes: {},
  tool_display: "on",
  guaranteed_dispatches: 4,
  created_at: "2026-11-15T10:00:00Z",
  updated_at: "2026-11-15T10:00:00Z",
})

const phoneNode = {
  node_id: "nod_phone",
  name: "phone",
  device: "android",
  connected: true,
  kvm: true,
  state: "running",
  installed: [],
}

type Frame = { key: boolean; data: Buffer }

// encode draws a phone's home screen with a sign-in card and encodes it as H.264 in Annex-B, as scrcpy sends it: a
// key frame (with its SPS and PPS) and two frames after it. Empty when this browser cannot encode H.264.
async function encode(page: Page): Promise<Frame[]> {
  const out = await page.evaluate<{ key: boolean; data: string }[]>(`(async () => {
    const width = 360, height = 640
    const config = { codec: "avc1.42E01F", width, height, avc: { format: "annexb" }, framerate: 10 }
    if (!("VideoEncoder" in window) || !(await VideoEncoder.isConfigSupported(config)).supported) return []

    const canvas = new OffscreenCanvas(width, height)
    const c = canvas.getContext("2d")
    const draw = (shade) => {
      const sky = c.createLinearGradient(0, 0, 0, height)
      sky.addColorStop(0, "#1e3a5f")
      sky.addColorStop(1, "#0f766e")
      c.fillStyle = sky
      c.fillRect(0, 0, width, height)
      c.fillStyle = "rgba(0,0,0,0.25)"
      c.fillRect(0, 0, width, 24)
      c.fillStyle = "#ffffff"
      c.font = "12px sans-serif"
      c.fillText("10:24", 14, 16)
      c.fillText("5G  86%", width - 70, 16)
      c.font = "bold 44px sans-serif"
      c.fillText("10:24", 24, 110)
      c.font = "14px sans-serif"
      c.fillText("Sat, Nov 15", 28, 136)
      c.fillStyle = "#ffffff"
      c.beginPath()
      c.roundRect(24, 200, width - 48, 220, 18)
      c.fill()
      c.fillStyle = "#18181b"
      c.font = "bold 18px sans-serif"
      c.fillText("Sign in", 44, 238)
      c.fillStyle = "#e4e4e7"
      c.fillRect(44, 256, width - 88, 40)
      c.fillRect(44, 308, width - 88, 40)
      c.fillStyle = shade
      c.fillRect(44, 362, width - 88, 40)
      const icons = ["#f97316", "#22c55e", "#3b82f6", "#eab308"]
      icons.forEach((color, i) => {
        c.fillStyle = color
        c.beginPath()
        c.arc(54 + i * 84, 560, 26, 0, Math.PI * 2)
        c.fill()
      })
      c.fillStyle = "rgba(255,255,255,0.8)"
      c.fillRect(width / 2 - 50, height - 14, 100, 4)
    }

    const chunks = []
    const encoder = new VideoEncoder({
      output: (chunk) => {
        const data = new Uint8Array(chunk.byteLength)
        chunk.copyTo(data)
        let text = ""
        data.forEach((b) => (text += String.fromCharCode(b)))
        chunks.push({ key: chunk.type === "key", data: btoa(text) })
      },
      error: () => {},
    })
    encoder.configure(config)
    const shades = ["#18181b", "#27272a", "#18181b"]
    for (let i = 0; i < shades.length; i++) {
      draw(shades[i])
      const frame = new VideoFrame(canvas, { timestamp: i * 100000 })
      encoder.encode(frame, { keyFrame: i === 0 })
      frame.close()
    }
    await encoder.flush()
    encoder.close()

    return chunks
  })()`)

  return out.map((f) => ({ key: f.key, data: Buffer.from(f.data, "base64") }))
}

// semu puts serve-emu's SEMU header (version 1) before an access unit, as the node sends it.
function semu(frame: Frame, pts: number) {
  const header = Buffer.alloc(16)
  header.write("SEMU", 0, "ascii")
  header[4] = 1
  header[5] = frame.key ? 1 : 0
  header.writeBigUInt64BE(BigInt(pts), 8)

  return Buffer.concat([header, frame.data])
}

type Options = { noPhone?: boolean; held?: boolean; frames?: Frame[]; waking?: boolean }

// fakeServer answers the API and plays the phone on the WebSocket: it says who controls, the phone's state, the
// screen's size and the frames; asked for a key frame it sends the key frame again; asked for control it gives it.
// As the control plane, it keeps the owner's hold until the owner hands it back, not when a view closes (issue
// #603). It keeps the WebSockets it was given and what the page sent.
async function fakeServer(page: Page, options: Options = {}) {
  const sockets: WebSocketRoute[] = []
  const sent: Record<string, unknown>[] = []
  let tickets = 0
  let held = options.held ?? false

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /device":
        return json({ data: options.noPhone ? [] : [phoneNode], held })
      case "POST /device/views":
        tickets++

        return json({ ticket: `ticket-${tickets}`, expires_at: new Date().toISOString() }, 201)
      case "GET /browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "GET /chats":
        return json({ data: [chat, other] })
      case "GET /bots":
        return json({ data: [bot("coordinator", 0, chat.id), bot("reporter", 1, other.id)] })
      case "GET /chats/cht_1":
        return json(chat)
      case "GET /chats/cht_2":
        return json(other)
      case "GET /chats/cht_1/stream": {
        const said = {
          seq: 1,
          chat_id: chat.id,
          role: "owner",
          text: "sign in to the shop app on the phone",
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

  const frames = options.frames ?? []
  const play = (ws: WebSocketRoute) => {
    frames.forEach((frame, i) => ws.send(semu(frame, i * 100_000)))
  }

  await page.routeWebSocket(/\/api\/v1\/device\/views\//, (ws) => {
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
      if (input.type === "reset-video" && frames.length > 0) play(ws)
    })
    ws.send(JSON.stringify({ type: "control", controller: held ? "you" : "agent" }))
    if (options.waking) {
      ws.send(JSON.stringify({ type: "state", state: "waking" }))

      return
    }
    ws.send(JSON.stringify({ type: "state", state: "running" }))
    ws.send(JSON.stringify({ type: "screen", width: 720, height: 1280 }))
    play(ws)
  })

  return { sockets, sent, of: (type: string) => sent.filter((m) => m.type === type) }
}

const screen = (page: Page) => page.getByTestId("phone-view").locator("canvas")
const painted = (page: Page) =>
  expect
    .poll(() =>
      page.evaluate<number>("document.querySelector('[data-testid=phone-view] canvas').width"),
    )
    .toBe(360)

async function openPhone(page: Page) {
  await page.getByRole("button", { name: "Open new" }).first().click()
  await page.getByRole("menuitem", { name: "Phone" }).click()
}

// expectFrameInside says that the phone's frame, side buttons included, is drawn whole inside its view.
async function expectFrameInside(page: Page) {
  const view = (await page.getByTestId("phone-view").boundingBox())!
  const frame = (await page.getByTestId("phone-frame").boundingBox())!
  expect(frame.width).toBeGreaterThan(80)
  expect(frame.x).toBeGreaterThanOrEqual(view.x)
  expect(frame.y).toBeGreaterThanOrEqual(view.y)
  expect(frame.x + frame.width).toBeLessThanOrEqual(view.x + view.width + 0.5)
  expect(frame.y + frame.height).toBeLessThanOrEqual(view.y + view.height + 0.5)
}

// Scenario: the owner opens the phone beside a chat to sign in to an app for the agents. "+" offers the phone; on a
// desktop it opens in a pane split off to the right of the chat, on a phone as a tab of the chat's pane. Its screen
// comes up in its frame and the owner only watches until taking control. Then a tap, a typed text, Enter, Home, the
// side buttons and the wheel reach the phone, and the owner hands it back.
test("the owner opens the phone beside a chat, takes control, and uses it", async ({
  page,
}, info) => {
  const desktop = info.project.name === "desktop"
  await page.goto("/")
  const frames = await encode(page)
  test.skip(frames.length === 0, "this browser cannot encode H.264 to play a phone's stream")

  const server = await fakeServer(page, { frames })
  await page.goto("/chats/cht_1")
  await expect(page.getByText("sign in to the shop app on the phone")).toBeVisible()

  await page.getByRole("button", { name: "Open new" }).click()
  await expect(page.getByRole("menuitem")).toHaveText(["New chat", "Phone"])
  await shot(page, "phone-open-new")
  await page.getByRole("menuitem", { name: "Phone" }).click()

  if (desktop) {
    // Beside the chat: a grid of two panes, the chat on the left and the phone on the right.
    await expect(page).toHaveURL(/\/grid$/)
    const chatPane = page.getByRole("region", { name: chat.title })
    const phonePane = page.getByRole("region", { name: "Phone" })
    await expect(chatPane.getByText("sign in to the shop app on the phone")).toBeVisible()
    await expect(phonePane.getByTestId("phone-view")).toBeVisible()
    const left = (await chatPane.boundingBox())!
    const right = (await phonePane.boundingBox())!
    expect(right.x).toBeGreaterThan(left.x + left.width - 1)
  }
  await expect(page.getByRole("tab", { name: "Phone" })).toHaveAttribute("aria-selected", "true")
  await painted(page)
  await expect(page.getByTestId("phone-view").getByRole("status")).toHaveCount(0)
  await expect(page.getByText("Watching the phone")).toBeVisible()
  await expectFrameInside(page)
  await shot(page, "phone-watching")

  // Watching: the phone takes nothing, and its buttons are off.
  await screen(page).click({ force: true })
  if (desktop) {
    await screen(page).hover({ force: true })
    await page.mouse.wheel(0, 120)
  }
  expect(server.of("touch")).toHaveLength(0)
  await expect(page.getByRole("button", { name: "Home" })).toBeDisabled()
  for (const name of ["Volume up", "Volume down", "Power"]) {
    await expect(page.getByRole("button", { name, exact: true })).toBeDisabled()
  }

  await page.getByRole("button", { name: /Take control/ }).click()
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  await expect(page.getByText("You are using the phone")).toBeVisible()

  // A tap in the middle of the picture is the middle of the screen, whatever the pane's size.
  const box = (await screen(page).boundingBox())!
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
  await expect.poll(() => server.of("touch").map((m) => m.action)).toEqual(["down", "up"])
  const down = server.of("touch")[0]
  expect(down.x as number).toBeCloseTo(0.5, 2)
  expect(down.y as number).toBeCloseTo(0.5, 2)

  await page.keyboard.type("an")
  await page.keyboard.press("Enter")
  await expect
    .poll(() => server.sent.filter((m) => m.type === "text" || m.type === "key"))
    .toEqual([
      { type: "text", text: "a" },
      { type: "text", text: "n" },
      { type: "key", keycode: 66 },
    ])

  await page.getByRole("button", { name: "Home" }).click()
  await expect.poll(() => server.of("home")).toHaveLength(1)

  // The side buttons: the volume as Android's keys 24 and 25, and Power.
  await page.getByRole("button", { name: "Volume up", exact: true }).click()
  await page.getByRole("button", { name: "Volume down", exact: true }).click()
  await page.getByRole("button", { name: "Power", exact: true }).click()
  await expect
    .poll(() =>
      server.sent.filter((m) => m.type === "power" || m.keycode === 24 || m.keycode === 25),
    )
    .toEqual([{ type: "key", keycode: 24 }, { type: "key", keycode: 25 }, { type: "power" }])
  if (desktop) {
    await page.getByRole("button", { name: "Volume up", exact: true }).hover()
    await expect(page.getByText("Volume up", { exact: true })).toBeVisible()
  }
  await shot(page, "phone-control")

  if (desktop) {
    // A wheel turned down over the screen is a finger dragged up, lifted when it stops.
    const touched = server.of("touch").length
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.wheel(0, 200)
    await expect.poll(() => server.of("touch").slice(touched).at(-1)?.action).toBe("up")
    const drag = server.of("touch").slice(touched)
    expect(drag[0]?.action).toBe("down")
    expect(drag.filter((m) => m.action === "move").length).toBeGreaterThan(1)
    // Every move goes up; at the top edge the finger lifts and comes down again lower, as Orca's does.
    drag.forEach((m, i) => {
      if (m.action === "move") expect(m.y as number).toBeLessThan(drag[i - 1]?.y as number)
    })
  }

  await page.getByRole("button", { name: /Hand back/ }).click()
  await expect(page.getByRole("button", { name: /Take control/ })).toBeVisible()
  expect(server.of("releaseControl")).toHaveLength(1)

  // The phone is gone to, not opened twice, and closes like any tab.
  if (!desktop) await page.getByRole("tab", { name: chat.title }).click()
  await page.getByRole("button", { name: "Open new" }).first().click()
  await page.getByRole("menuitem", { name: "Go to the phone" }).click()
  await expect(page.getByRole("tab", { name: "Phone" })).toHaveCount(1)
  await expect(page.getByRole("tab", { name: "Phone" })).toHaveAttribute("aria-selected", "true")
  await page.getByRole("button", { name: "Close Phone" }).click()
  await expect(page.getByRole("tab", { name: "Phone" })).toHaveCount(0)
})

// In a narrow pane of a small screen the frame is made smaller to fit, not cut, and the bar above it keeps its
// control button inside the pane.
test("the phone's frame fits a narrow pane", async ({ page }, info) => {
  test.skip(info.project.name === "phone", "a phone has no grid")
  await page.setViewportSize({ width: 960, height: 540 })
  await page.goto("/")
  const frames = await encode(page)
  test.skip(frames.length === 0, "this browser cannot encode H.264 to play a phone's stream")

  await fakeServer(page, { frames })
  await page.goto("/chats/cht_1")
  await openPhone(page)
  await expect(page.getByRole("region", { name: "Phone" })).toBeVisible()
  await painted(page)
  await expectFrameInside(page)
  const view = (await page.getByTestId("phone-view").boundingBox())!
  const control = page.getByRole("button", { name: "Take control" })
  await expect(control).toHaveText("Take control", { useInnerText: true })
  const button = (await control.boundingBox())!
  expect(button.x + button.width).toBeLessThanOrEqual(view.x + view.width - 4)
  await shot(page, "phone-narrow")
})

// Scenario: the owner holds the phone beside a chat and drags another Bot's main chat into the grid. The phone's pane moves
// over but stays as it was: its view is not closed and opened again, which would give the phone back to the agents
// (issue #597).
test("a chat added to the grid leaves the phone held and connected", async ({ page }, info) => {
  test.skip(info.project.name === "phone", "a phone has no grid")
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await openPhone(page)
  await expect.poll(() => server.sockets).toHaveLength(1)
  let closed = 0
  server.sockets[0].onClose(() => closed++)
  await page.getByRole("button", { name: /Take control/ }).click()
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()

  const box = (await page.getByRole("region", { name: chat.title }).boundingBox())!
  await page
    .getByRole("navigation", { name: "Bots" })
    .getByRole("link", { name: other.title })
    .hover()
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2, box.y + box.height * 0.97, { steps: 12 })
  await page.mouse.up()
  await expect(page.getByRole("region", { name: other.title })).toBeVisible()

  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  expect(closed).toBe(0)
  expect(server.sockets).toHaveLength(1)
})

// Scenario: the owner holds the phone and looks at another tab of the phone's pane, then comes back. The phone's
// view closes, but the page does not hand the phone back: only "Hand back" does, and the control plane keeps the
// hold meanwhile (issue #603). The sidebar still says the owner holds it, and the phone comes back held.
test("switching to another tab of the pane keeps the owner's hold on the phone", async ({
  page,
}, info) => {
  test.skip(info.project.name === "phone", "a phone has no sidebar")
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await openPhone(page)
  await expect.poll(() => server.sockets).toHaveLength(1)
  let closed = 0
  server.sockets[0].onClose(() => closed++)
  await page.getByRole("button", { name: /Take control/ }).click()
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  await expect(page.getByText("You hold the phone.")).toBeVisible({ timeout: 10_000 })

  const phonePane = page.getByRole("region", { name: "Phone" })
  await phonePane.getByRole("button", { name: "Open new" }).click()
  await page.getByRole("menuitem", { name: "New chat" }).click()
  // With several Bots, New chat asks which.
  await page.getByRole("menuitem", { name: "coordinator" }).click()
  await expect(page.getByTestId("phone-view")).toHaveCount(0)
  await expect.poll(() => closed).toBe(1)
  expect(server.of("releaseControl")).toHaveLength(0)
  await expect(page.getByText("You hold the phone.")).toBeVisible()

  await page.getByRole("tab", { name: "Phone" }).click()
  await expect.poll(() => server.sockets).toHaveLength(2)
  await expect(page.getByRole("button", { name: /Hand back/ })).toBeVisible()
  await expect(page.getByText("You hold the phone.")).toBeVisible()
})

// While the phone's machine starts, the view says so on the phone's card.
test("a phone whose machine is waking says so", async ({ page }) => {
  await fakeServer(page, { waking: true })
  await page.goto("/chats/cht_1")
  await openPhone(page)

  await expect(page.getByTestId("phone-view")).toContainText("Waking the phone's machine")
  await shot(page, "phone-waking")
})

// A phone that cannot be shown is said to be, in the control plane's words, and is not tried again until the owner
// asks.
test("a phone that cannot be shown says so", async ({ page }) => {
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await openPhone(page)
  await expect.poll(() => server.sockets).toHaveLength(1)

  await server.sockets[0].close({
    code: 4404,
    reason: "the phone is not available: the phone's node phone is offline",
  })
  await expect(page.getByRole("alert")).toContainText("the phone's node phone is offline")
  await shot(page, "phone-gone")

  await page.getByRole("button", { name: "Reconnect" }).click()
  await expect.poll(() => server.sockets).toHaveLength(2)
})

// A control plane where no node holds a phone offers none.
test("a control plane without a phone offers none", async ({ page }) => {
  await fakeServer(page, { noPhone: true })
  await page.goto("/chats/cht_1")

  await page.getByRole("button", { name: "Open new" }).click()
  await expect(page.getByRole("menuitem")).toHaveText(["New chat"])
  await shot(page, "phone-none")
})

// While the owner holds the phone the sidebar says so, on every page.
test("the sidebar says that the owner holds the phone", async ({ page }, info) => {
  test.skip(info.project.name === "phone", "a phone has no sidebar")
  await fakeServer(page, { held: true })
  await page.goto("/routines")

  await expect(page.getByText("You hold the phone.")).toBeVisible()
  await shot(page, "sidebar-phone-held")
})
