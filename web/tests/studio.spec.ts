import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { deflateSync } from "node:zlib"

import { expect, test, type Page, type Route } from "@playwright/test"

import { shot } from "./shot.js"

// The Studio (issue #818, row 12), built, against a fake client API, on a phone and a desktop: the library of takes
// and videos with its filters, search, layouts and pages, a take's page with Make video, and a video's page that
// follows its revision's stages and scenes, then plays it for review, takes a change request that makes revision 2,
// and approves it. The video is a real H.264 MP4 the node's writer made (fixtures/take, as takes.spec.ts plays).

const clip = readFileSync(fileURLToPath(new URL("./fixtures/take/seg-001.mp4", import.meta.url)))

// png is a picture of a phone's screen for a thumbnail: a bar on top and rows, in a colour of its own.
function png(hue: [number, number, number]): Buffer {
  const width = 90
  const height = 160
  const raw = Buffer.alloc((width * 3 + 1) * height)
  for (let y = 0; y < height; y++) {
    const row = y * (width * 3 + 1)
    for (let x = 0; x < width; x++) {
      const bar = y < 18
      const line = y > 30 && y % 22 < 10 && x > 10 && x < 80
      const [r, g, b] = bar ? hue : line ? [225, 228, 235] : [250, 250, 252]
      raw.set([r, g, b], row + 1 + x * 3)
    }
  }

  const chunk = (type: string, data: Buffer) => {
    const len = Buffer.alloc(4)
    len.writeUInt32BE(data.length)
    const body = Buffer.concat([Buffer.from(type), data])
    const crc = Buffer.alloc(4)
    crc.writeUInt32BE(crc32(body))

    return Buffer.concat([len, body, crc])
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width, 0)
  header.writeUInt32BE(height, 4)
  header.set([8, 2, 0, 0, 0], 8)

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ])
}

function crc32(data: Buffer): number {
  let c = ~0
  for (const byte of data) {
    c ^= byte
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1))
  }

  return ~c >>> 0
}

const takeId = "tk_0123456789abcdef"
const videoId = "vid_signin"
const failedVideo = "vid_checkout"
const lines = ["Mở ứng dụng cửa hàng", "Chạm vào Đăng nhập", "Trang tài khoản hiện ra"]

type Item = {
  kind: "take" | "video"
  id: string
  title: string
  bot: string
  created_at: string
  length_ms: number
  state: string
  take?: string
  rev?: number
  thumbnail?: string
}

const at = (minutes: number) => new Date(Date.UTC(2026, 10, 18, 9, minutes)).toISOString()

// The library: a take filmed now, one paused, one uploading, one ready with its video awaiting review, one failed,
// an approved video and one that failed, and enough older takes for a second page.
function library(): Item[] {
  const items: Item[] = [
    {
      kind: "take",
      id: "tk_00000000000000a1",
      title: "Thanh toán bằng ví",
      bot: "Atlas",
      created_at: at(59),
      length_ms: 42_000,
      state: "recording",
    },
    {
      kind: "video",
      id: videoId,
      title: "Đăng nhập cửa hàng",
      bot: "Atlas",
      created_at: at(58),
      length_ms: 21_400,
      state: "review",
      take: takeId,
      rev: 1,
      thumbnail: `/api/v1/videos/${videoId}/revisions/1/thumbnail.jpg`,
    },
    {
      kind: "take",
      id: takeId,
      title: "Đăng nhập cửa hàng",
      bot: "Atlas",
      created_at: at(55),
      length_ms: 13_000,
      state: "available",
      thumbnail: `/api/v1/takes/${takeId}/thumbnail.jpg`,
    },
    {
      kind: "take",
      id: "tk_00000000000000a2",
      title: "Đặt lịch giao hàng",
      bot: "worker 41",
      created_at: at(54),
      length_ms: 30_000,
      state: "paused",
    },
    {
      kind: "video",
      id: "vid_search",
      title: "Tìm sản phẩm",
      bot: "Nova",
      created_at: at(50),
      length_ms: 18_200,
      state: "approved",
      take: "tk_00000000000000a3",
      rev: 2,
      thumbnail: "/api/v1/videos/vid_search/revisions/2/thumbnail.jpg",
    },
    {
      kind: "video",
      id: failedVideo,
      title: "Checkout",
      bot: "Nova",
      created_at: at(48),
      length_ms: 0,
      state: "failed",
      take: "tk_00000000000000a4",
      rev: 1,
    },
    {
      kind: "take",
      id: "tk_00000000000000a5",
      title: "Mở thông báo",
      bot: "Nova",
      created_at: at(45),
      length_ms: 0,
      state: "failed",
    },
    {
      kind: "take",
      id: "tk_00000000000000a6",
      title: "Đổi mật khẩu",
      bot: "worker 41",
      created_at: at(44),
      length_ms: 9000,
      state: "uploading",
    },
  ]

  for (let n = 0; n < 26; n++) {
    items.push({
      kind: "take",
      id: `tk_00000000000001${String(n).padStart(2, "0")}`,
      title: `Lượt quay cũ ${n + 1}`,
      bot: "Atlas",
      created_at: at(40 - n),
      length_ms: 5000 + n * 1000,
      state: "available",
      thumbnail: `/api/v1/takes/tk_00000000000001${String(n).padStart(2, "0")}/thumbnail.jpg`,
    })
  }

  return items
}

const groupOf = (item: Item): string => {
  if (item.kind === "take") {
    if (item.state === "available") return ""

    return item.state === "failed" ? "failed" : "in_progress"
  }
  if (["queued", "voicing", "composing", "captioning"].includes(item.state)) return "in_progress"
  if (item.state === "review" || item.state === "approved") return item.state

  return "failed"
}

// order is the server's: the newest first, then by id, then by kind.
const order = (a: Item, b: Item) =>
  b.created_at.localeCompare(a.created_at) ||
  a.id.localeCompare(b.id) ||
  a.kind.localeCompare(b.kind)

const take = {
  id: takeId,
  title: "Đăng nhập cửa hàng",
  agent: "b-cht_1",
  agent_name: "Atlas",
  node: "nod_phone",
  started_at: at(55),
  state: "available",
  ended_by: "stop",
  duration_ms: 3000,
  segments: [
    {
      file: "seg-001.mp4",
      at_ms: 0,
      length_ms: 3000,
      last_frame_ms: 2500,
      width: 64,
      height: 112,
      bytes: clip.length,
      sha256: "",
    },
  ],
  steps: lines.map((text, i) => ({ at_ms: i * 900, text })),
  pauses: [],
}

type Revision = {
  rev: number
  parent?: number
  state: string
  lines: string[]
  captions: boolean
  aspect: string
  length_ms: number
  warnings: string[]
  note?: string
  failed_stage?: string
  failed_cause?: string
  progress?: { scenes_done: number; scenes_total: number }
  created_at: string
  updated_at: string
}

type Server = {
  items: Item[]
  revisions: Revision[]
  made: Record<string, unknown>[]
  makeKeys: string[]
  changes: Record<string, unknown>[]
  approved: number[]
  retried: { rev: number; key: string }[]
  failedRevisions: Revision[]
  studioCalls: string[]
}

function failedRevision(rev: number, more: Partial<Revision> = {}): Revision {
  return revision(rev, "failed", {
    captions: true,
    aspect: "9:16",
    failed_stage: "voicing",
    failed_cause: "scene 1: soniox: the key was refused; elevenlabs: no key",
    lines: ["Mở giỏ hàng", "Chạm vào Thanh toán"],
    ...more,
  })
}

function failedVideoOf(server: Server) {
  return {
    id: failedVideo,
    take: "tk_00000000000000a4",
    title: "Checkout",
    made_by: "d-41",
    made_by_name: "worker 41",
    created_at: at(48),
    revisions: server.failedRevisions,
  }
}

function revision(rev: number, state: string, more: Partial<Revision> = {}): Revision {
  return {
    rev,
    state,
    lines,
    captions: false,
    aspect: "16:9",
    length_ms: state === "review" || state === "approved" ? 3000 : 0,
    warnings: [],
    created_at: at(58),
    updated_at: at(58),
    ...more,
  }
}

async function fakeServer(page: Page): Promise<Server> {
  const server: Server = {
    items: library(),
    revisions: [],
    made: [],
    makeKeys: [],
    changes: [],
    approved: [],
    retried: [],
    failedRevisions: [failedRevision(1)],
    studioCalls: [],
  }
  const video = () => ({
    id: videoId,
    take: takeId,
    title: "Đăng nhập cửa hàng",
    made_by: "owner",
    made_by_name: "owner",
    created_at: at(58),
    revisions: server.revisions,
    ...(server.revisions.find((r) => r.state === "approved")
      ? { approved: server.revisions.find((r) => r.state === "approved")!.rev }
      : {}),
  })

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (/thumbnail\.jpg$/.test(path)) {
      const hue: [number, number, number] = path.includes("videos") ? [37, 99, 235] : [16, 163, 127]

      return route.fulfill({ status: 200, contentType: "image/png", body: png(hue) })
    }
    if (
      /^GET \/(videos\/[^/]+\/revisions\/\d+\/video\.mp4|takes\/[^/]+\/segments\/.+)$/.test(call)
    ) {
      return route.fulfill({
        status: 200,
        headers: { "content-type": "video/mp4", "accept-ranges": "bytes" },
        body: clip,
      })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /bots":
        return json({ data: [{ id: "coordinator", name: "Atlas", main: true }] })
      case "GET /studio/items": {
        server.studioCalls.push(url.search)
        const kind = url.searchParams.get("kind")
        const state = url.searchParams.get("state")
        const q = (url.searchParams.get("q") ?? "").toLowerCase()
        const limit = Number(url.searchParams.get("limit") ?? 50)
        const cursor = url.searchParams.get("cursor")
        let list = [...server.items]
          .filter((i) => !kind || i.kind === kind)
          .filter((i) => !state || groupOf(i) === state)
          .filter((i) => !q || i.title.toLowerCase().includes(q) || i.bot.toLowerCase().includes(q))
          .sort(order)
        if (cursor) {
          const [time, id, k] = cursor.split("|") as [string, string, Item["kind"]]
          const last = { created_at: time, id, kind: k } as Item
          list = list.filter((i) => order(i, last) > 0)
        }
        const pageItems = list.slice(0, limit)
        const tail = pageItems.at(-1)

        return json({
          data: pageItems,
          ...(list.length > limit && tail
            ? { next_cursor: `${tail.created_at}|${tail.id}|${tail.kind}` }
            : {}),
        })
      }
      case `GET /takes/${takeId}`:
        return json(take)
      case "GET /videos":
        return json({ data: server.revisions.length > 0 ? [video()] : [] })
      case "POST /videos": {
        server.made.push(request.postDataJSON() as Record<string, unknown>)
        server.makeKeys.push(request.headers()["idempotency-key"] ?? "")
        server.revisions = [revision(1, "queued")]

        return json(video(), 201)
      }
      case `GET /videos/${videoId}`:
        return json(video())
      case `POST /videos/${videoId}/revisions/1/changes`: {
        const body = request.postDataJSON() as { lines?: string[]; note?: string }
        server.changes.push(body)
        server.revisions = [
          { ...server.revisions[0]!, state: "changes_requested", note: body.note },
          revision(2, "queued", { parent: 1, lines: body.lines ?? lines }),
        ]

        return json(video())
      }
      case `POST /videos/${videoId}/revisions/2/approve`:
        server.approved.push(2)
        server.revisions = server.revisions.map((r) =>
          r.rev === 2 ? { ...r, state: "approved" } : r,
        )

        return json(video())
      case `POST /videos/${failedVideo}/revisions/1/retry`:
      case `POST /videos/${failedVideo}/revisions/2/retry`: {
        // A retry the server answers as it does: the video, its new revision failing again at once. A key used
        // before for another revision is refused, as the server refuses it (idempotency_key_conflict).
        const from = Number(path.split("/")[4])
        const key = request.headers()["idempotency-key"] ?? ""
        if (server.retried.some((r) => r.key === key && r.rev !== from)) {
          return json({ code: "idempotency_key_conflict", message: "the request id was used" }, 409)
        }
        server.retried.push({ rev: from, key })
        server.failedRevisions.push(
          failedRevision(server.failedRevisions.length + 1, { parent: from }),
        )

        return json(failedVideoOf(server), 201)
      }
      case `GET /videos/${failedVideo}`:
        return json(failedVideoOf(server))
      default:
        return json({ data: [] })
    }
  })

  return server
}

const cards = (page: Page) => page.getByTestId("studio-item")

// Scenario: the owner opens the Studio from Places. Its cards show each take and video with its first frame, kind,
// state and length; the take filmed now has a red dot in place of a frame. The chips keep the videos, or those
// awaiting review; a search finds a title in Vietnamese or a Bot, made by the server before it cuts a page. The
// table shows the same. The first page has 30; Show more reads the rest after the cursor, once each, though a take
// was filmed meanwhile.
test("the Studio lists, filters, searches and pages its takes and videos", async ({
  page,
}, info) => {
  const phone = info.project.name === "phone"
  const server = await fakeServer(page)
  await page.goto("/")

  if (phone) await page.getByRole("button", { name: "Menu" }).click()
  await page
    .getByRole("navigation", { name: "Places" })
    .getByRole("link", { name: "Studio" })
    .click()
  await expect(page).toHaveURL(/\/studio$/)
  await expect(page.getByRole("heading", { name: "Studio" })).toBeVisible()

  await expect(cards(page)).toHaveCount(30)
  const filmed = page.getByRole("article", { name: "Thanh toán bằng ví" })
  await expect(filmed.getByTestId("filming-dot")).toHaveText("REC")
  await expect(filmed).toContainText("Filming")
  await expect(filmed).toContainText("0:42")
  const review = page.getByRole("article", { name: "Đăng nhập cửa hàng" }).first()
  await expect(review).toContainText("Video · rev 1")
  await expect(review).toContainText("Awaiting review")
  await expect(review.locator("img")).toHaveAttribute(
    "src",
    `/api/v1/videos/${videoId}/revisions/1/thumbnail.jpg`,
  )
  await expect(page.getByRole("article", { name: "Đặt lịch giao hàng" })).toContainText("PAUSED")
  await shot(page, "studio-grid")

  // A take filmed between the pages is not read twice, nor does it push one onto the next page.
  server.items.push({
    kind: "take",
    id: "tk_00000000000000ff",
    title: "Quay mới",
    bot: "Atlas",
    created_at: at(59 + 1),
    length_ms: 1000,
    state: "recording",
  })
  await page.getByRole("button", { name: "Show more" }).click()
  await expect(page.getByRole("button", { name: "Show more" })).toHaveCount(0)
  const shown = await cards(page).evaluateAll((all) => all.map((a) => a.getAttribute("data-id")))
  expect(new Set(shown).size).toBe(shown.length)
  expect(shown).toContain("take:tk_0000000000000125")

  await page
    .getByRole("radio", { name: "Videos" })
    .or(page.getByRole("button", { name: "Videos" }))
    .click()
  await expect(cards(page)).toHaveCount(3)
  await page
    .getByRole("radio", { name: "Awaiting review" })
    .or(page.getByRole("button", { name: "Awaiting review" }))
    .click()
  await expect(cards(page)).toHaveCount(1)
  await expect(cards(page).first()).toHaveAccessibleName("Đăng nhập cửa hàng")
  expect(server.studioCalls.at(-1)).toContain("state=review")
  await shot(page, "studio-filtered")

  await page
    .getByRole("radio", { name: "All" })
    .or(page.getByRole("button", { name: "All", exact: true }))
    .click()
  await page
    .getByRole("radio", { name: "Any state" })
    .or(page.getByRole("button", { name: "Any state" }))
    .click()
  await page.getByRole("searchbox", { name: "Search titles and Bots" }).fill("đăng")
  await expect(cards(page)).toHaveCount(2)
  await page.getByRole("searchbox", { name: "Search titles and Bots" }).fill("worker 41")
  await expect(cards(page)).toHaveCount(2)
  await page.getByRole("searchbox", { name: "Search titles and Bots" }).fill("")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("table")).toBeVisible()
  await expect(page.getByRole("row").filter({ hasText: "Checkout" })).toContainText("Failed")
  await shot(page, "studio-table")
  await page.getByRole("button", { name: "grid" }).click()

  await page.getByRole("link", { name: "Lượt quay cũ 1", exact: true }).click()
  await expect(page).toHaveURL(/\/studio\/takes\/tk_0000000000000100$/)
})

// Scenario: the owner opens a take's own page, makes it a video in 16:9 without captions, and the video's page
// follows revision 1: queued, then voiced and cut scene by scene, then made. The owner plays it, edits a line, writes
// a note and asks for changes, which makes revision 2; once it is made the owner approves it, and the switch shows
// revision 1 with the note. A video that failed says where and why, with Make again.
test("a take is made into a video the owner reviews and approves", async ({ page }) => {
  const server = await fakeServer(page)
  await page.goto(`/studio/takes/${takeId}`)

  const takeView = page.getByTestId("take-view")
  await expect(takeView).toBeVisible()
  await expect(page.getByRole("link", { name: "Studio" }).first()).toBeVisible()
  await expect(takeView.getByTestId("take-video")).toBeVisible()
  await shot(page, "studio-take")
  await takeView.getByRole("button", { name: "Make video" }).click()

  const dialog = page.getByRole("dialog", { name: "Make a video" })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole("switch", { name: "Captions" })).toBeChecked()
  await dialog.getByRole("switch", { name: "Captions" }).click()
  await dialog.getByRole("button", { name: "16:9, wide" }).click()
  await expect(dialog).toContainText("1280 × 720")
  await shot(page, "studio-make-video")
  await dialog.getByRole("button", { name: "Make video" }).click()

  await expect(page).toHaveURL(new RegExp(`/studio/videos/${videoId}$`))
  expect(server.made).toEqual([{ take: takeId, captions: false, aspect: "16:9" }])
  expect(server.makeKeys[0]).toMatch(/^[0-9a-f-]{36}$/)

  const view = page.getByTestId("video-view")
  const progress = view.getByTestId("video-progress")
  await expect(progress).toContainText("Revision 1 is queued")

  server.revisions = [revision(1, "voicing", { progress: { scenes_done: 1, scenes_total: 3 } })]
  await expect(progress).toContainText("Making revision 1: Voice", { timeout: 5000 })
  await expect(progress).toContainText("1 of 3 scenes")

  server.revisions = [revision(1, "composing", { progress: { scenes_done: 2, scenes_total: 3 } })]
  await expect(progress).toContainText("Making revision 1: Cut and fit", { timeout: 5000 })
  await expect(view.locator("[data-testid=video-scene][data-done]")).toHaveCount(2)
  await expect(progress.locator("li[data-status=skipped]")).toContainText("Captions")
  await shot(page, "studio-video-making")

  server.revisions = [revision(1, "review")]
  const player = view.getByTestId("video-player")
  await expect(player).toBeVisible({ timeout: 5000 })
  await expect(player).toHaveAttribute("src", `/api/v1/videos/${videoId}/revisions/1/video.mp4`)
  await expect
    .poll(() => player.evaluate((v) => (v as unknown as { readyState: number }).readyState))
    .toBeGreaterThan(0)
  await expect(view.getByRole("button", { name: "Download the video" })).toHaveAttribute(
    "href",
    `/api/v1/videos/${videoId}/revisions/1/video.mp4`,
  )
  const review = view.getByRole("complementary", { name: "Review" })
  await expect(review.getByRole("textbox", { name: "Scene 2" })).toHaveValue("Chạm vào Đăng nhập")
  await shot(page, "studio-video-review")

  await review.getByRole("textbox", { name: "Scene 2" }).fill("Chạm nút Đăng nhập ở góc phải")
  await review.getByRole("textbox", { name: "Note for the agent" }).fill("Câu 2 nói rõ chỗ chạm")
  await expect(review).toContainText("the voice of each line kept is reused")
  await expect(review.getByRole("button", { name: "Approve" })).toBeDisabled()
  await shot(page, "studio-video-changes")
  await review.getByRole("button", { name: "Request changes" }).click()

  await expect
    .poll(() => server.changes)
    .toEqual([
      {
        lines: [lines[0], "Chạm nút Đăng nhập ở góc phải", lines[2]],
        note: "Câu 2 nói rõ chỗ chạm",
      },
    ])
  await expect(progress).toContainText("Revision 2 is queued")

  server.revisions = [
    server.revisions[0]!,
    { ...server.revisions[1]!, state: "review", length_ms: 3200 },
  ]
  await expect(review.getByRole("button", { name: "Approve" })).toBeVisible({ timeout: 5000 })
  await expect(review).toContainText("from revision 1")
  await expect(review.getByRole("textbox", { name: "Scene 2" })).toHaveValue(
    "Chạm nút Đăng nhập ở góc phải",
  )
  await review.getByRole("button", { name: "Approve" }).click()
  await expect.poll(() => server.approved).toEqual([2])
  await expect(review).toContainText("Approved. An approved revision is never changed")
  await expect(review.getByRole("textbox", { name: "Scene 1" })).toBeDisabled()
  await shot(page, "studio-video-approved")

  await view.getByRole("combobox", { name: "Revision" }).click()
  await page.getByRole("option", { name: "Revision 1 · Changes asked" }).click()
  await expect(review).toContainText("Câu 2 nói rõ chỗ chạm")
  await expect(view.getByTestId("video-player")).toHaveAttribute(
    "src",
    `/api/v1/videos/${videoId}/revisions/1/video.mp4`,
  )
  await shot(page, "studio-video-revisions")

  await page.goto(`/studio/videos/${failedVideo}`)
  const failed = page.getByTestId("video-progress")
  await expect(failed).toContainText("Revision 1 failed")
  await expect(failed).toContainText("soniox: the key was refused")
  await expect(failed.locator("li[data-status=failed]")).toContainText("Voice")
  await expect(failed.getByRole("button", { name: "Make again" })).toBeVisible()
  await shot(page, "studio-video-failed")

  // Make again makes the failed revision as it was asked, by its own route, not by a new make (#937). When that
  // one fails too, Make again of it is another request, with its own key.
  const made = server.made.length
  await failed.getByRole("button", { name: "Make again" }).click()
  await expect(failed).toContainText("Revision 2 failed")
  await failed.getByRole("button", { name: "Make again" }).click()
  await expect(failed).toContainText("Revision 3 failed")
  expect(server.retried.map((r) => r.rev)).toEqual([1, 2])
  expect(server.retried[0]!.key).not.toEqual(server.retried[1]!.key)
  expect(server.retried.every((r) => r.key !== "")).toBe(true)
  expect(server.made).toHaveLength(made)
})
