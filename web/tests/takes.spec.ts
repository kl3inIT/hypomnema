import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

import { expect, test, type Page, type Route, type WebSocketRoute } from "@playwright/test"

import { shot } from "./shot.js"

// Takes an agent filmed of the phone (issue #817), built, against a fake client API: the take's card in the chat
// as its state goes, the take as a tab that plays its two segments on one timeline and seeks across them and into a
// still tail, and the phone's tab saying REC and PAUSED while a take is filmed. The segments are real H.264 MP4s
// the node's writer made (internal/node/device, fixture_test.go).

const chat = {
  id: "cht_1",
  title: "film the shop's sign-in",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: "2026-11-18T10:00:00Z",
  updated_at: "2026-11-18T10:00:00Z",
}

const phoneNode = {
  node_id: "nod_phone",
  name: "phone",
  device: "android",
  connected: true,
  kvm: true,
  state: "running",
  installed: [],
}

const takeId = "tk_0123456789abcdef"
const failedId = "tk_00000000000000ff"

const segments: Record<string, Buffer> = Object.fromEntries(
  ["seg-001.mp4", "seg-002.mp4"].map((name) => [
    name,
    readFileSync(fileURLToPath(new URL(`./fixtures/take/${name}`, import.meta.url))),
  ]),
)

// The take: a first segment of 3 s whose last frame is at 2.5 s, the owner's takeover at 3 s, and a second of
// 10 s whose last frame is at 1.5 s, a still tail of 8.5 s after it.
function takeOf(state: string) {
  return {
    id: takeId,
    title: "Sign in to the shop",
    agent: "b-cht_1",
    agent_name: "bot coordinator in chat cht_1",
    node: "nod_phone",
    app_package: "vn.shop.app",
    app_version: "3.1.0",
    started_at: "2026-11-18T10:01:00Z",
    state,
    ...(state === "available" || state === "uploading" ? { ended_by: "stop" } : {}),
    duration_ms: state === "recording" ? 4200 : 13_000,
    segments:
      state === "available"
        ? [
            {
              file: "seg-001.mp4",
              at_ms: 0,
              length_ms: 3000,
              last_frame_ms: 2500,
              width: 64,
              height: 112,
              bytes: segments["seg-001.mp4"]!.length,
              sha256: "",
            },
            {
              file: "seg-002.mp4",
              at_ms: 3000,
              length_ms: 10_000,
              last_frame_ms: 1500,
              width: 64,
              height: 112,
              bytes: segments["seg-002.mp4"]!.length,
              sha256: "",
            },
          ]
        : [],
    steps: [
      { at_ms: 400, text: "Open the shop app", chapter: "Open the app" },
      { at_ms: 2600, text: "Tap Sign in", tap: { x: 320, y: 610 } },
      { at_ms: 5000, text: "Type the email", chapter: "Sign in" },
      { at_ms: 11_000, text: "The account page shows" },
    ],
    pauses: [{ at_ms: 3000 }],
  }
}

const failedTake = {
  ...takeOf("failed"),
  id: failedId,
  title: "Film the checkout",
  state: "failed",
  ended_by: "stop",
  reason: "no frame from the phone",
  duration_ms: 0,
  steps: [],
  pauses: [],
}

// A Bot's turn that filmed: video_start, two lines, video_stop; and a start of another take that failed.
const messages = [
  {
    role: "tool",
    text: "pi ran video_start",
    parts: [
      {
        type: "tool_result",
        id: "t1",
        name: "video_start",
        args: { title: "Sign in to the shop", request_id: "r1" },
        output: `Take ${takeId} ("Sign in to the shop") is recording, 0:00 long, with 0 steps.`,
        duration_ms: 900,
      },
      {
        type: "tool_result",
        id: "t2",
        name: "video_say",
        args: { take: takeId, text: "Tap Sign in", tap: "#3" },
        output: `Said at 0:02 of take ${takeId}.`,
        duration_ms: 40,
      },
      {
        type: "tool_result",
        id: "t3",
        name: "video_stop",
        args: { take: takeId },
        output: `Take ${takeId} is finalizing.`,
        duration_ms: 60,
      },
    ],
  },
  { role: "coordinator", text: "I filmed the sign-in; the take uploads now." },
  {
    role: "tool",
    text: "pi ran video_start",
    parts: [
      {
        type: "tool_result",
        id: "t4",
        name: "video_start",
        args: { title: "Film the checkout" },
        output: `Take ${failedId} ("Film the checkout") is recording.`,
        duration_ms: 700,
      },
    ],
  },
]

// serveSegment answers a segment's request as the control plane does, with ranges (http.ServeContent).
function serveSegment(route: Route, data: Buffer) {
  const range = route.request().headers()["range"]
  const headers = { "accept-ranges": "bytes", "content-type": "video/mp4" }
  const asked = range && /bytes=(\d+)-(\d*)/.exec(range)
  if (!asked) return route.fulfill({ status: 200, headers, body: data })

  const start = Number(asked[1])
  const end = asked[2] ? Math.min(Number(asked[2]), data.length - 1) : data.length - 1

  return route.fulfill({
    status: 206,
    headers: { ...headers, "content-range": `bytes ${start}-${end}/${data.length}` },
    body: data.subarray(start, end + 1),
  })
}

// The video made of the take in a pane (issue #818): queued, its lines the take's steps.
const madeVideo = {
  id: "vid_signin",
  take: takeId,
  title: "Sign in to the shop",
  made_by: "owner",
  made_by_name: "owner",
  created_at: "2026-11-18T10:05:00Z",
  revisions: [
    {
      rev: 1,
      state: "queued",
      lines: ["Open the shop app", "Tap Sign in", "Type the email", "The account page shows"],
      captions: true,
      aspect: "9:16",
      length_ms: 0,
      warnings: [],
      created_at: "2026-11-18T10:05:00Z",
      updated_at: "2026-11-18T10:05:00Z",
    },
  ],
}

type Server = {
  made?: boolean
  state: string
  deleted: string[]
  filming: (message: object) => void
  views: () => number
}

async function fakeServer(page: Page): Promise<Server> {
  const server: Server = { state: "recording", deleted: [], filming: () => {}, views: () => 0 }

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const segment = /^GET \/takes\/tk_[0-9a-f]+\/segments\/(seg-\d+\.mp4)$/.exec(call)
    if (segment) return serveSegment(route, segments[segment[1]!]!)

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /device":
        return json({ data: [phoneNode], held: false })
      case "POST /device/views":
        return json({ ticket: "ticket-1", expires_at: new Date().toISOString() }, 201)
      case "GET /browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "GET /bots":
        return json({ data: [{ id: "coordinator", name: "Atlas", main: true }] })
      case "GET /chats":
        return json({ data: [chat] })
      case "GET /chats/cht_1":
        return json(chat)
      case `GET /takes/${takeId}`:
        return server.deleted.includes(takeId)
          ? json({ code: "not_found", message: "no such take" }, 404)
          : json(takeOf(server.state))
      case `GET /takes/${failedId}`:
        return json(failedTake)
      case "GET /videos":
        return json({ data: server.made ? [madeVideo] : [] })
      // The Look's catalogue, as the server starts it (#938); its sample is answered by the default below.
      case "GET /videos/looks":
        return json({
          presets: [{ id: "clean", name: "Clean" }],
          backgrounds: ["midnight"],
          chromes: ["full", "none", "android", "iphone"],
          default: {
            captions: { preset: "clean", position: "preset" },
            frame: { chrome: "android", padding: 48, radius: 40, shadow: true, safe_top: false },
            background: { preset: "midnight" },
          },
        })
      case "POST /videos":
        server.made = true

        return json(madeVideo, 201)
      case `GET /videos/${madeVideo.id}`:
        return json(madeVideo)
      case `DELETE /takes/${takeId}`:
        server.deleted.push(takeId)

        return route.fulfill({ status: 204 })
      case "GET /chats/cht_1/stream": {
        const all = [{ role: "owner", text: "film the sign-in of the shop app" }, ...messages]

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

  const sockets: WebSocketRoute[] = []
  server.views = () => sockets.length
  server.filming = (message) => {
    for (const ws of sockets) {
      try {
        ws.send(JSON.stringify({ type: "take", ...message }))
      } catch {
        // A view that closed, as when the pane moved to the grid.
      }
    }
  }

  await page.routeWebSocket(/\/api\/v1\/device\/views\//, (ws) => {
    sockets.push(ws)
    ws.send(JSON.stringify({ type: "control", controller: "agent" }))
    ws.send(JSON.stringify({ type: "state", state: "running" }))
    ws.send(JSON.stringify({ type: "screen", width: 720, height: 1280 }))
  })

  return server
}

const paneTab = (page: Page, name: string | RegExp) =>
  page.getByRole("tablist", { name: "Tabs of this pane" }).getByRole("tab", { name })
const time = (page: Page) => page.getByTestId("take-time")
const playing = (page: Page) => page.getByTestId("take-video")

// seconds is the timeline's time the player shows now.
async function seconds(page: Page): Promise<number> {
  const [m, s] = ((await time(page).textContent()) ?? "0:00").split(" / ")[0]!.split(":")

  return Number(m) * 60 + Number(s)
}

// Scenario: the Bot filmed the sign-in. Its video_start shows a card under its work that follows the take: filming,
// then uploading, then ready with "Open"; a take that got no frame says why. Open opens the take as a tab beside the
// chat (in the pane on a phone): the player has the steps beside it or under it, a mark for each step and a hatched
// mark at the owner's pause. It plays across the boundary of its two segments; a step seeks into the second; the
// last step seeks into its still tail, from where it plays on to the end. Delete asks first, then closes the tab.
test("a take's card follows it, and its tab plays and seeks it", async ({ page }, info) => {
  const phone = info.project.name === "phone"
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")

  const card = page.getByRole("region", { name: "Take: Sign in to the shop" })
  await expect(card).toContainText("Filming")
  await expect(page.getByText("Started filming Film the checkout")).toBeVisible()
  await shot(page, "take-card-filming")

  server.state = "uploading"
  await expect(card).toContainText("Uploading from the phone's machine", { timeout: 10_000 })
  await expect(card.getByRole("button", { name: "Open" })).toHaveCount(0)
  await shot(page, "take-card-uploading")

  const failed = page.getByRole("region", { name: "Take: Film the checkout" })
  await expect(failed).toContainText("Failed: no frame from the phone")

  server.state = "available"
  await expect(card).toContainText("Ready to watch · 0:13 · 4 steps", { timeout: 10_000 })
  await shot(page, "take-card-ready")

  await card.getByRole("button", { name: "Open" }).click()
  await expect(paneTab(page, /Sign in to the shop/)).toHaveAttribute("aria-selected", "true")
  if (!phone) await expect(page).toHaveURL(/\/grid$/)

  const view = page.getByTestId("take-view")
  await expect(view).toBeVisible()
  await expect(view.getByTestId("take-mark")).toHaveCount(4)
  await expect(view.getByTestId("take-pause")).toHaveCount(1)
  await expect(view.getByRole("button", { name: "0:00 Open the app", exact: true })).toBeVisible()
  await expect(view.getByRole("button", { name: "0:05 Sign in", exact: true })).toBeVisible()
  await expect(time(page)).toHaveText("0:00 / 0:13")
  await expect
    .poll(() => playing(page).evaluate((v) => (v as unknown as { readyState: number }).readyState))
    .toBeGreaterThan(0)
  await shot(page, "take-tab")

  // Played from just before the end of the first segment, it goes on in the second.
  await view.getByRole("button", { name: /Tap Sign in/ }).click()
  await expect(time(page)).toHaveText("0:02 / 0:13")
  await view.getByRole("button", { name: "Play" }).click()
  await expect(playing(page)).toHaveAttribute("data-segment", "seg-002.mp4", { timeout: 5000 })
  await expect.poll(() => seconds(page), { timeout: 5000 }).toBeGreaterThanOrEqual(3)
  await view.getByRole("button", { name: "Pause" }).click()

  // A step seeks into the second segment.
  await view.getByRole("button", { name: /Type the email/ }).click()
  await expect(time(page)).toHaveText("0:05 / 0:13")
  await expect(view.getByRole("button", { name: /Type the email/ })).toHaveAttribute(
    "aria-current",
    "step",
  )

  // The last step is in the still tail of the second segment: it seeks there and plays on, never back to its start.
  await view.getByRole("button", { name: /The account page shows/ }).click()
  await expect(time(page)).toHaveText("0:11 / 0:13")
  await view.getByRole("button", { name: "Play" }).click()
  await expect.poll(() => seconds(page), { timeout: 5000 }).toBeGreaterThanOrEqual(12)
  expect(await seconds(page)).toBeGreaterThanOrEqual(11)
  await expect(view.getByRole("button", { name: "Play" })).toBeVisible({ timeout: 5000 })
  await expect(time(page)).toHaveText("0:13 / 0:13")

  // The timeline seeks back across the boundary into the first segment.
  await view.getByRole("slider", { name: "Where in the take" }).fill("1000")
  await expect(playing(page)).toHaveAttribute("data-segment", "seg-001.mp4")
  await expect(time(page)).toHaveText("0:01 / 0:13")

  await expect(view.getByRole("button", { name: "Download the take" })).toHaveAttribute(
    "href",
    `/api/v1/takes/${takeId}/archive`,
  )

  await view.getByRole("button", { name: "Delete" }).click()
  const confirm = page.getByRole("alertdialog", { name: "Delete this take?" })
  await expect(confirm).toBeVisible()
  await shot(page, "take-delete")
  await confirm.getByRole("button", { name: "Delete the take" }).click()
  await expect.poll(() => server.deleted).toEqual([takeId])
  await expect(paneTab(page, /Sign in to the shop/)).toHaveCount(0)
})

// Scenario: the owner makes the take a video from its tab (issue #818): Make video asks for the voice, captions and
// shape, and the video opens as a tab of the same pane, queued; the take's tab then has "Video", and the video's
// tab "Take", each going to the other.
test("a take's tab makes a video that opens as a tab of the pane", async ({ page }) => {
  const server = await fakeServer(page)
  server.state = "available"
  await page.goto("/chats/cht_1")

  const card = page.getByRole("region", { name: "Take: Sign in to the shop" })
  await card.getByRole("button", { name: "Open" }).click()
  const view = page.getByTestId("take-view")
  await view.getByRole("button", { name: "Make video" }).click()
  const dialog = page.getByRole("dialog", { name: "Make a video" })
  await dialog.getByRole("button", { name: "Make video" }).click()

  // The take's tab and the video's, both named by the title.
  await expect(paneTab(page, /Sign in to the shop/)).toHaveCount(2)
  await expect(paneTab(page, /Sign in to the shop/).last()).toHaveAttribute(
    "title",
    /A video of a take/,
  )
  await expect(paneTab(page, /Sign in to the shop/).last()).toHaveAttribute("aria-selected", "true")
  const video = page.getByTestId("video-view")
  await expect(video.getByTestId("video-progress")).toContainText("Revision 1 is queued")
  await expect(video.getByTestId("video-scene")).toHaveCount(4)

  await video.getByRole("button", { name: "Open its take" }).click()
  await expect(view.getByRole("button", { name: "Video", exact: true })).toBeVisible()
  await view.getByRole("button", { name: "Video", exact: true }).click()
  await expect(page.getByTestId("video-view")).toBeVisible()
})

// Scenario: an agent films the phone while the owner watches it. The phone's bar says REC with the take's time and
// the last line said; when the owner takes control the take is PAUSED; once the take ends the bar is the plain one.
test("the phone's tab says REC and PAUSED while a take is filmed", async ({ page }) => {
  const server = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await page.getByRole("button", { name: "Open new" }).first().click()
  await page.getByRole("menuitem", { name: "Phone" }).click()

  const bar = page.getByTestId("phone-view")
  await expect(bar.getByText("Watching the phone")).toBeVisible()
  await expect.poll(() => server.views()).toBeGreaterThan(0)
  await expect(bar.getByText("Connecting video…")).toBeVisible()

  server.filming({
    take: takeId,
    title: "Sign in to the shop",
    agent: "bot coordinator in chat cht_1",
    state: "recording",
    at_ms: 61_000,
    at: new Date().toISOString(),
    said: "Tap Sign in",
  })
  const badge = page.getByTestId("take-badge")
  await expect(badge).toContainText("REC 1:01")
  await expect(badge).toContainText("“Tap Sign in”")
  await expect(badge).toContainText("REC 1:02", { timeout: 3000 })
  await shot(page, "phone-rec")

  server.filming({
    take: takeId,
    title: "Sign in to the shop",
    agent: "bot coordinator in chat cht_1",
    state: "paused",
    at_ms: 63_000,
    at: new Date().toISOString(),
    said: "Tap Sign in",
  })
  await expect(badge).toContainText("PAUSED 1:03")
  await shot(page, "phone-paused")

  server.filming({ take: "" })
  await expect(badge).toHaveCount(0)
  await expect(bar.getByText("Watching the phone")).toBeVisible()
})
