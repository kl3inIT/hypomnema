import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { deflateSync } from "node:zlib"

import { expect, test, type Locator, type Page, type Route } from "@playwright/test"

import { flac, mp3, ogg, wav } from "./audio.js"
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
// turnedTake is a take whose phone turned while it was filmed: a frame sent for it is refused, a frame from the
// default falls back to full (issue #908, row 11).
const turnedTake = "tk_00000000000000a7"
// silentTake is a take the agent said no line on: it has no steps, so it cannot be made into a video (#1028).
const silentTake = "tk_000000000000051e"
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
  look: Look
  created_at: string
  updated_at: string
}

type Look = {
  captions: { preset: string; position: string }
  frame: { chrome: string; padding: number; radius: number; shadow: boolean; safe_top: boolean }
  background?: { preset?: string; colors?: string[] }
  fell_back_from?: string
  music?: {
    track: string
    name?: string
    length_ms?: number
    gain_db: number
    fell_back_from?: { track: string; name: string; length_ms: number }
  }
}

const frameOf = (chrome: string) => ({
  chrome,
  padding: 48,
  radius: chrome === "android" || chrome === "iphone" ? 40 : 14,
  shadow: true,
  safe_top: false,
})

// savedLook is the saved default of the catalogue: an owner's make chose an iPhone frame, so choosing Android is a
// change, and the default is a frame that falls back on a turned take.
// lofi is the saved default's track (issue #910).
const lofi = "mus_00000000000010f1"

const savedLook: Look = {
  captions: { preset: "clean", position: "preset" },
  frame: frameOf("iphone"),
  background: { preset: "midnight" },
}

const catalogue = {
  presets: [
    ["one-word", "One Word"],
    ["word-pair", "Word Pair"],
    ["pop", "Pop"],
    ["karaoke", "Karaoke"],
    ["highlight-yellow", "Highlight"],
    ["highlight-green", "Pop Green"],
    ["clean", "Clean"],
    ["bold", "Bold"],
    ["minimal", "Minimal"],
    ["boxed", "Boxed"],
    ["mono", "Mono"],
    ["bar", "Bar"],
    ["legacy", "Classic"],
  ].map(([id, name]) => ({ id, name })),
  backgrounds: [
    "macos",
    "macos-light",
    "midnight",
    "sunset",
    "ocean",
    "forest",
    "peach",
    "aurora",
    "grid",
    "mono",
    "light",
  ],
  chromes: ["full", "none", "android", "iphone"],
  // The saved default lays the owner's lo-fi track under the voice (issue #910): a look sent without its music keeps it.
  default: {
    ...savedLook,
    music: { track: lofi, name: "Lo-fi buổi sáng", length_ms: 95_000, gain_db: 0 },
  },
}

// The owner's music library (issue #910): a track of each format the Studio plays, a corrupt one, one whose file is
// missing. Their audio is made by tests/audio.ts, an m4a by the browser's own recorder.
type MusicTrack = {
  id: string
  name: string
  media_type: string
  length_ms: number
  bytes: number
  created_at: string
  missing?: boolean
}

const track = (id: string, name: string, media_type: string, more: Partial<MusicTrack> = {}) => ({
  id,
  name,
  media_type,
  length_ms: 95_000,
  bytes: 2_400_000,
  created_at: at(40),
  ...more,
})

function musicLibrary(): MusicTrack[] {
  return [
    track(lofi, "Lo-fi buổi sáng", "audio/mpeg"),
    track("mus_000000000000f1ac", "Piano", "audio/flac", { length_ms: 184_000, bytes: 21_700_000 }),
    track("mus_00000000000000cc", "Ukulele", "audio/ogg", { length_ms: 62_000 }),
    track("mus_0000000000000ba5", "Bass", "audio/wav", { length_ms: 30_000, bytes: 5_300_000 }),
    track("mus_00000000000004a4", "Guitar", "audio/mp4", { length_ms: 121_000 }),
    track("mus_0000000000000bad", "Broken", "audio/mpeg", { length_ms: 48_000 }),
    track("mus_000000000000d15c", "Old jingle", "audio/mpeg", { missing: true, length_ms: 15_000 }),
  ]
}

const audioOf: Record<string, () => Buffer> = {
  [lofi]: mp3,
  mus_000000000000f1ac: flac,
  mus_00000000000000cc: ogg,
  mus_0000000000000ba5: wav,
  // Bytes that are no track: the player shows its error state.
  mus_0000000000000bad: () => Buffer.from("this is not an mp3, only its name says so".repeat(40)),
}

type Upload = { name: string; type: string; bytes: number }

// sampleHue is a look sample's colour, its body's own, so a test can tell which request's answer is shown.
function sampleHue(body: string): [number, number, number] {
  let h = 0
  for (const c of body) h = (h * 31 + c.charCodeAt(0)) >>> 0

  return [h & 0xff, (h >> 8) & 0xff, (h >> 16) & 0xff]
}

type Sample = { body: Record<string, unknown>; aborted: boolean }

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
  samples: Sample[]
  // sampleAnswer answers a sample in place of the picture: a delay first, or a refusal.
  sampleAnswer: (body: Record<string, unknown>) => { delay?: number; status?: number }
  looks: { body: Record<string, unknown>; key: string }[]
  // looksRead counts the reads of the looks' catalogue.
  looksRead: number
  music: MusicTrack[]
  usedBytes: number
  // uploads are the tracks sent to the upload route; uploadAnswer answers one: a delay first, a refusal, or 201.
  uploads: Upload[]
  uploadAnswer: (upload: Upload) => { delay?: number; status?: number; body?: string }
  deleted: string[]
  // deleteAnswer refuses a delete with its status, or deletes it (0).
  deleteAnswer: (id: string) => number
  // m4a is an m4a the browser recorded, served for the m4a track.
  m4a?: Buffer
  // defaultLook is the saved default GET /videos/looks answers.
  defaultLook: Look
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
    look: {
      captions: { preset: "clean", position: "preset" },
      frame: frameOf("android"),
      background: { preset: "midnight" },
    },
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
    samples: [],
    sampleAnswer: () => ({}),
    looks: [],
    looksRead: 0,
    music: musicLibrary(),
    usedBytes: 61_300_000,
    uploads: [],
    uploadAnswer: () => ({}),
    deleted: [],
    deleteAnswer: () => 0,
    defaultLook: catalogue.default,
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

  page.on("requestfailed", (request) => {
    if (!request.url().endsWith("/api/v1/videos/preview")) return
    const sample = server.samples.find((s) => JSON.stringify(s.body) === (request.postData() ?? ""))
    if (sample) sample.aborted = true
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

    const audio = /^GET \/videos\/music\/([^/]+)\/audio$/.exec(call)
    if (audio) {
      const id = audio[1]!
      const t = server.music.find((m) => m.id === id)
      const body = id === "mus_00000000000004a4" ? server.m4a : audioOf[id]?.()
      if (!t || !body) return json({ code: "not_found", message: `music track ${id}` }, 404)

      return route.fulfill({
        status: 200,
        headers: { "content-type": t.media_type, "x-content-type-options": "nosniff" },
        body,
      })
    }
    const removed = /^DELETE \/videos\/music\/([^/]+)$/.exec(call)
    if (removed) {
      const id = removed[1]!
      const status = server.deleteAnswer(id)
      if (status === 409) {
        return json(
          {
            code: "conflict",
            message: `a revision being made has music track ${id}; delete it once it is made`,
          },
          409,
        )
      }
      server.deleted.push(id)
      server.music = server.music.filter((m) => m.id !== id)

      return route.fulfill({ status: 204 })
    }

    switch (call) {
      case "GET /videos/music":
        return json({
          tracks: server.music,
          used_bytes: server.usedBytes,
          quota_bytes: 5_000_000_000,
        })
      case "POST /videos/music": {
        const upload = {
          name: url.searchParams.get("name") ?? "",
          type: request.headers()["content-type"] ?? "",
          bytes: request.postDataBuffer()?.length ?? 0,
        }
        server.uploads.push(upload)
        const answer = server.uploadAnswer(upload)
        if (answer.delay) await new Promise((done) => setTimeout(done, answer.delay))
        if (answer.status) {
          return route.fulfill({
            status: answer.status,
            contentType: answer.body?.startsWith("{") ? "application/json" : "text/html",
            body: answer.body ?? "",
          })
        }
        const made = track(
          `mus_${"0".repeat(12)}${(server.music.length + 0xa0).toString(16)}`,
          upload.name,
          "audio/mpeg",
          {
            created_at: at(59),
            length_ms: 72_000,
            bytes: upload.bytes,
          },
        )
        server.music = [made, ...server.music]
        server.usedBytes += upload.bytes

        return json(made, 201)
      }
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
      case `GET /takes/${turnedTake}`:
        return json({ ...take, id: turnedTake, title: "Xoay ngang" })
      case `GET /takes/${silentTake}`:
        return json({ ...take, id: silentTake, title: "Thử luồng nộp địa điểm", steps: [] })
      case "GET /videos/looks":
        server.looksRead++

        return json({ ...catalogue, default: server.defaultLook })
      case "POST /videos/preview": {
        const raw = request.postData() ?? ""
        const body = JSON.parse(raw) as { take: string; look?: { frame?: unknown } }
        server.samples.push({ body, aborted: false })
        const answer = server.sampleAnswer(body)
        if (answer.delay) await new Promise((done) => setTimeout(done, answer.delay))
        if (body.take === turnedTake && body.look?.frame) {
          return json({ code: "conflict", message: "the phone turned during the take" }, 409)
        }
        if (answer.status) {
          const refusal: Record<number, [string, string]> = {
            429: ["rate_limited", "another look sample is being made; try again in a second"],
            503: ["unavailable", "ffmpeg is not installed"],
            504: ["timed_out", "the look sample took too long: longer than 20s"],
          }
          const [code, message] = refusal[answer.status]!

          return json({ code, message }, answer.status)
        }

        // The page may have dropped the request meanwhile; then nothing is answered.
        return route
          .fulfill({ status: 200, contentType: "image/png", body: png(sampleHue(raw)) })
          .catch(() => {})
      }
      case "GET /videos":
        return json({ data: server.revisions.length > 0 ? [video()] : [] })
      case "POST /videos": {
        const body = request.postDataJSON() as { take: string; look?: { frame?: unknown } }
        server.made.push(body)
        server.makeKeys.push(request.headers()["idempotency-key"] ?? "")
        if (body.take === turnedTake) {
          if (body.look?.frame) {
            return json(
              {
                code: "conflict",
                message: "the phone turned during the take, so it cannot be framed",
              },
              409,
            )
          }
          // The default's frame falls back to full on this revision only, with a warning.
          server.revisions = [
            revision(1, "queued", {
              look: { ...savedLook, frame: frameOf("full"), fell_back_from: "iphone" },
              warnings: ["the phone turned during the take, so the video has no iphone frame"],
            }),
          ]

          return json({ ...video(), take: turnedTake }, 201)
        }
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
      case `POST /videos/${videoId}/revisions/1/look`: {
        const body = request.postDataJSON() as { look: Partial<Look> }
        server.looks.push({ body, key: request.headers()["idempotency-key"] ?? "" })
        const source = server.revisions[0]!
        server.revisions = [
          ...server.revisions,
          revision(server.revisions.length + 1, "queued", {
            parent: 1,
            look: { ...source.look, ...(body.look as Look) },
          }),
        ]

        return json(video(), 201)
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

// shownHue is the colour of the look sample shown (the page's DOM, as a string: the tests have no DOM types).
const shownHue = (page: Page) =>
  page.evaluate(`(() => {
    const img = document.querySelector("[data-testid=look-sample] img")
    if (!img || !img.complete || !img.naturalWidth) return null
    const canvas = document.createElement("canvas")
    canvas.width = img.naturalWidth
    canvas.height = img.naturalHeight
    const g = canvas.getContext("2d")
    g.drawImage(img, 0, 0)
    return Array.from(g.getImageData(5, 5, 1, 1).data.slice(0, 3))
  })()`)

// showSample scrolls the dialog until the look sample is at its top, so a picture taken on a phone shows it.
const showSample = (look: Locator) =>
  look
    .getByTestId("look-sample")
    .evaluate((el) =>
      (el as unknown as { scrollIntoView: (o: object) => void }).scrollIntoView({ block: "start" }),
    )

const lookOf = (sample: Sample) => (sample.body as { look?: unknown }).look

// Scenario: the owner opens a take the agent said no line on (#1028). Make video is disabled, described by the
// line that says why and how to get steps, in place of the server's refusal; the looks and their sample are never
// read.
test("a take with no steps cannot be made into a video and says why", async ({ page }) => {
  const server = await fakeServer(page)
  await page.goto(`/studio/takes/${silentTake}`)

  const takeView = page.getByTestId("take-view")
  const make = takeView.getByRole("button", { name: "Make video" })
  await expect(make).toBeDisabled()
  await expect(make).toHaveAccessibleDescription(
    /cannot be made into a video\. Ask the agent to film it again, saying each step/,
  )
  const steps = takeView.getByRole("complementary", { name: "Steps" })
  await expect(steps).toContainText("0 steps")
  await expect(steps).toContainText("so this take cannot be made into a video")
  await expect(takeView.getByTestId("take-video")).toBeVisible()
  await shot(page, "studio-take-no-steps")
  expect(server.looksRead).toBe(0)
  expect(server.samples).toEqual([])
})

// Scenario: the owner makes a take into a video and opens its Look (issue #908, row 16; #938): it starts from the
// saved default, with a sample of it. Changing the frame twice drops the first sample asked and shows only the
// latest; a sample the server cannot draw says why (busy, no ffmpeg, too long). A background chosen and set back is
// not sent; the make carries only the frame changed.
test("Make video sends only the look sections changed, with a sample of the latest", async ({
  page,
}) => {
  const server = await fakeServer(page)
  await page.goto(`/studio/takes/${takeId}`)
  await page.getByTestId("take-view").getByRole("button", { name: "Make video" }).click()

  const dialog = page.getByRole("dialog", { name: "Make a video" })
  const look = dialog.getByRole("region", { name: "Look" })
  await expect(look.getByRole("button", { name: "iPhone" })).toHaveAttribute("aria-pressed", "true")
  await expect(look.getByRole("combobox", { name: "Style" })).toHaveText(/Clean/)
  await expect(look.getByRole("img", { name: "A sample of the look" })).toBeVisible()
  expect(server.samples[0]!.body).toEqual({ take: takeId, aspect: "9:16", captions: true })
  await expect(look.getByTestId("background-swatch")).toHaveCount(11)
  await look.scrollIntoViewIfNeeded()
  await shot(page, "studio-make-look")
  await look.getByRole("button", { name: "Your two colours" }).scrollIntoViewIfNeeded()
  await shot(page, "studio-make-look-background")

  // A sample still drawing is dropped when the owner changes the look again: only the latest one is shown.
  server.sampleAnswer = (body) => (JSON.stringify(body).includes('"none"') ? { delay: 3000 } : {})
  await look.getByRole("button", { name: "Plain" }).click()
  await expect.poll(() => server.samples.length).toBe(2)
  await look.getByRole("button", { name: "Android" }).click()
  await expect.poll(() => server.samples.length).toBe(3)
  const latest = server.samples[2]!
  expect(lookOf(latest)).toEqual({ frame: { chrome: "android", shadow: true, safe_top: false } })
  await expect.poll(() => server.samples[1]!.aborted).toBe(true)
  await expect.poll(() => shownHue(page)).toEqual(sampleHue(JSON.stringify(latest.body)))
  await page.waitForTimeout(3500)
  await expect.poll(() => shownHue(page)).toEqual(sampleHue(JSON.stringify(latest.body)))

  // A sample the server refuses says why.
  const refusals: Record<string, number> = { ocean: 429, sunset: 503, forest: 504 }
  server.sampleAnswer = (body) => {
    const preset = (body as { look?: { background?: { preset?: string } } }).look?.background
      ?.preset

    return preset && refusals[preset] ? { status: refusals[preset] } : {}
  }
  const sample = look.getByTestId("look-sample")
  await look.getByRole("button", { name: "Ocean" }).click()
  await expect(sample.getByRole("alert")).toHaveText(/another look sample is being made/)
  await sample.scrollIntoViewIfNeeded()
  await shot(page, "studio-look-sample-busy")
  await look.getByRole("button", { name: "Sunset" }).click()
  await expect(sample.getByRole("alert")).toHaveText(/ffmpeg is not installed/)
  await showSample(look)
  await shot(page, "studio-look-sample-unavailable")
  await look.getByRole("button", { name: "Forest" }).click()
  await expect(sample.getByRole("alert")).toHaveText(/took too long/)
  await showSample(look)
  await shot(page, "studio-look-sample-timed-out")
  await look.getByRole("button", { name: "Midnight" }).click()
  await expect(sample.getByRole("alert")).toHaveCount(0)
  await expect(look.getByRole("img", { name: "A sample of the look" })).toBeVisible()

  await dialog.getByRole("button", { name: "Make video" }).click()
  await expect(page).toHaveURL(new RegExp(`/studio/videos/${videoId}$`))
  expect(server.made).toEqual([
    {
      take: takeId,
      captions: true,
      aspect: "9:16",
      look: { frame: { chrome: "android", shadow: true, safe_top: false } },
    },
  ])
})

// Scenario: the take's phone turned while it was filmed. Choosing the Android frame is refused in the sample and at
// Make, with the server's reason; set back to the default's frame, the make sends no frame and succeeds, the
// default's frame falling back to full with a warning (row 11).
test("on a take whose phone turned, the default look is made and a chosen frame is refused", async ({
  page,
}) => {
  const server = await fakeServer(page)
  await page.goto(`/studio/takes/${turnedTake}`)
  await page.getByTestId("take-view").getByRole("button", { name: "Make video" }).click()

  const dialog = page.getByRole("dialog", { name: "Make a video" })
  const look = dialog.getByRole("region", { name: "Look" })
  await expect(look.getByRole("img", { name: "A sample of the look" })).toBeVisible()
  await look.getByRole("button", { name: "Android" }).click()
  await expect(look.getByTestId("look-sample").getByRole("alert")).toHaveText(/the phone turned/)
  await dialog.getByRole("button", { name: "Make video" }).click()
  await expect(dialog.getByRole("alert").last()).toHaveText(/cannot be framed/)
  expect((server.made[0] as { look?: unknown }).look).toEqual({
    frame: { chrome: "android", shadow: true, safe_top: false },
  })
  await dialog.getByRole("alert").last().scrollIntoViewIfNeeded()
  await shot(page, "studio-make-turned-refused")

  await look.getByRole("button", { name: "iPhone" }).click()
  await expect(look.getByRole("img", { name: "A sample of the look" })).toBeVisible()
  await showSample(look)
  await shot(page, "studio-make-turned-default")
  await dialog.getByRole("button", { name: "Make video" }).click()
  await expect(page).toHaveURL(new RegExp(`/studio/videos/${videoId}$`))
  expect(server.made[1]).toEqual({ take: turnedTake, captions: true, aspect: "9:16" })

  server.revisions = [{ ...server.revisions[0]!, state: "review", length_ms: 3000 }]
  const review = page.getByRole("complementary", { name: "Review" })
  await expect(review).toContainText("so the video has no iphone frame", { timeout: 5000 })
  await shot(page, "studio-make-turned-made")
})

// Scenario: the owner opens Change look on a made revision. It starts from the revision's look, the sample is drawn
// from that revision (rev), and the captions, off in it, are not chosen. A background changed is the only section
// posted to the look route, with an idempotency key; revision 2 is queued from revision 1.
test("Change look posts only the sections changed and the new revision appears", async ({
  page,
}) => {
  const server = await fakeServer(page)
  server.revisions = [revision(1, "review")]
  await page.goto(`/studio/videos/${videoId}`)

  const review = page.getByRole("complementary", { name: "Review" })
  await review.getByRole("button", { name: "Change look" }).click()
  const dialog = page.getByRole("dialog", { name: "Change the look" })
  const look = dialog.getByRole("region", { name: "Look" })
  await expect(look.getByRole("button", { name: "Android" })).toHaveAttribute(
    "aria-pressed",
    "true",
  )
  await expect(look.getByRole("combobox", { name: "Style" })).toBeDisabled()
  await expect(look).toContainText("Captions are off.")
  await expect(look.getByRole("img", { name: "A sample of the look" })).toBeVisible()
  expect(server.samples[0]!.body).toEqual({
    take: takeId,
    aspect: "16:9",
    captions: false,
    rev: 1,
  })
  await expect(dialog.getByRole("button", { name: "Change look" })).toBeDisabled()
  await showSample(look)
  await shot(page, "studio-change-look-start")

  await look.getByRole("button", { name: "Your two colours" }).click()
  await expect(look.getByLabel("Top left")).toHaveValue("#1b2140")
  await look.getByLabel("Bottom right").scrollIntoViewIfNeeded()
  await shot(page, "studio-change-look-colours")
  await look.getByRole("button", { name: "Ocean" }).click()
  await expect
    .poll(() => lookOf(server.samples.at(-1)!))
    .toEqual({ background: { preset: "ocean" } })
  expect(server.samples.at(-1)!.body).toMatchObject({ rev: 1 })
  await expect(look.getByRole("img", { name: "A sample of the look" })).toBeVisible()
  await showSample(look)
  await shot(page, "studio-change-look")
  await dialog.getByRole("button", { name: "Change look" }).click()

  await expect(dialog).toBeHidden()
  expect(server.looks).toHaveLength(1)
  expect(server.looks[0]!.body).toEqual({ look: { background: { preset: "ocean" } } })
  expect(server.looks[0]!.key).toMatch(/^[0-9a-f-]{36}$/)
  await expect(page.getByTestId("video-progress")).toContainText("Revision 2 is queued")
})

// The Music group of the Look (issue #910, row 18). An m4a to play is recorded by the browser itself after the page
// was pressed, as a page may start sound only then.
test.describe("the Music group", () => {
  // openMake opens Make video on the take, with its Look's Music group.
  async function openMake(page: Page) {
    await page.goto(`/studio/takes/${takeId}`)
    await page.getByTestId("take-view").getByRole("button", { name: "Make video" }).click()
    const dialog = page.getByRole("dialog", { name: "Make a video" })
    const group = dialog.getByRole("group", { name: "Music" })
    await expect(group.getByRole("radio").first()).toBeVisible()

    return { dialog, group }
  }

  // showGroup scrolls the dialog to the group, so a picture taken on a phone shows it.
  const showGroup = (group: Locator) =>
    group.evaluate((el) =>
      (el as unknown as { scrollIntoView: (o: object) => void }).scrollIntoView({ block: "start" }),
    )

  const radio = (group: Locator, name: string) =>
    group.getByRole("radio", { name: new RegExp(`^${name}`) })

  // recordM4a is a second of a tone as the browser's MediaRecorder writes it in MP4, or null when it cannot.
  async function recordM4a(page: Page): Promise<Buffer | null> {
    const base64 = (await page.evaluate(`(async () => {
      const type = ["audio/mp4;codecs=mp4a.40.2", "audio/mp4"].find((t) => MediaRecorder.isTypeSupported(t))
      if (!type) return null
      const ctx = new AudioContext()
      await ctx.resume()
      if (ctx.state !== "running") return null
      const tone = ctx.createOscillator()
      const out = ctx.createMediaStreamDestination()
      tone.connect(out)
      tone.start()
      const recorder = new MediaRecorder(out.stream, { mimeType: type })
      const parts = []
      recorder.ondataavailable = (e) => parts.push(e.data)
      const stopped = new Promise((done) => (recorder.onstop = done))
      recorder.start()
      await new Promise((done) => setTimeout(done, 1200))
      recorder.stop()
      await stopped
      const bytes = new Uint8Array(await new Blob(parts).arrayBuffer())
      let text = ""
      for (const b of bytes) text += String.fromCharCode(b)
      return btoa(text)
    })()`)) as string | null

    return base64 ? Buffer.from(base64, "base64") : null
  }

  // Scenario: the owner opens Make video; the Music group lists the library from GET /videos/music, the saved
  // default's track picked, with what the library holds. Each track plays: mp3, flac, ogg and wav; m4a plays where
  // the browser says it can (canPlayType), else shows the error state; a corrupt file shows the error state; a track
  // whose file is missing says so and does not play. A delete asks first, and a refusal says why. Nothing in the
  // music changed, the make sends no music, so the server keeps the default's.
  test("the library lists, plays each format and deletes a track after a confirm", async ({
    page,
  }) => {
    const server = await fakeServer(page)
    const { dialog, group } = await openMake(page)

    await expect(radio(group, "Lo-fi buổi sáng")).toBeChecked()
    await expect(group.getByTestId("music-usage")).toHaveText("61.3 MB of 5 GB used")
    const missing = group.getByTestId("music-row").filter({ hasText: "Old jingle" })
    await expect(missing).toContainText("missing")
    await expect(missing).toContainText("Its file is missing")
    await expect(missing.getByRole("button", { name: "Play Old jingle" })).toBeDisabled()
    await expect(radio(group, "Old jingle")).toBeDisabled()
    await showGroup(group)
    await shot(page, "studio-music-list")

    for (const name of ["Lo-fi buổi sáng", "Piano", "Ukulele", "Bass"]) {
      await group.getByRole("button", { name: `Play ${name}` }).click()
      await expect(group.getByRole("button", { name: `Pause ${name}` })).toBeVisible()
    }
    await group.getByRole("button", { name: "Pause Bass" }).click()
    await expect(group.getByRole("button", { name: "Play Bass" })).toBeVisible()

    const guitar = group.getByTestId("music-row").filter({ hasText: "Guitar" })
    const mp4 = (await page.evaluate(
      `document.createElement("audio").canPlayType("audio/mp4")`,
    )) as string
    if (mp4) {
      server.m4a = (await recordM4a(page)) ?? undefined
      test.skip(!server.m4a, "this browser plays m4a but cannot record one to play")
      await guitar.getByRole("button", { name: "Play Guitar" }).click()
      await expect(guitar.getByRole("button", { name: "Pause Guitar" })).toBeVisible()
    } else {
      await guitar.getByRole("button", { name: "Play Guitar" }).click()
      await expect(guitar.getByRole("alert")).toHaveText("This browser cannot play this track.")
    }

    const broken = group.getByTestId("music-row").filter({ hasText: "Broken" })
    await broken.getByRole("button", { name: "Play Broken" }).click()
    await expect(broken.getByRole("alert")).toHaveText("This browser cannot play this track.")
    await expect(broken.getByRole("button", { name: "Play Broken" })).toBeDisabled()
    await missing.scrollIntoViewIfNeeded()
    await shot(page, "studio-music-play-error")

    await group.getByRole("button", { name: "Delete Piano" }).click()
    const confirm = page.getByRole("alertdialog", { name: "Delete “Piano”?" })
    await expect(confirm).toContainText("Videos made with it keep their sound.")
    await shot(page, "studio-music-delete")
    expect(server.deleted).toEqual([])
    await confirm.getByRole("button", { name: "Delete" }).click()
    await expect(confirm).toBeHidden()
    expect(server.deleted).toEqual(["mus_000000000000f1ac"])
    await expect(radio(group, "Piano")).toHaveCount(0)

    server.deleteAnswer = () => 409
    await group.getByRole("button", { name: "Delete Bass" }).click()
    const refused = page.getByRole("alertdialog", { name: "Delete “Bass”?" })
    await refused.getByRole("button", { name: "Delete" }).click()
    await expect(refused.getByRole("alert")).toContainText("a revision being made has music track")
    await shot(page, "studio-music-delete-refused")
    await refused.getByRole("button", { name: "Keep it" }).click()
    await expect(radio(group, "Bass")).toBeVisible()

    await dialog.getByRole("button", { name: "Make video" }).click()
    await expect(page).toHaveURL(new RegExp(`/studio/videos/${videoId}$`))
    expect(server.made).toEqual([{ take: takeId, captions: true, aspect: "9:16" }])
  })

  // Scenario: the owner picks another track and raises its level. The look sample is not asked again, since music
  // does not change the picture; the make sends the music as one section, its track with its gain.
  test("a track chosen is sent with its gain, and the sample is not asked again", async ({
    page,
  }) => {
    const server = await fakeServer(page)
    const { dialog, group } = await openMake(page)
    await expect(dialog.getByRole("img", { name: "A sample of the look" })).toBeVisible()
    expect(server.samples).toHaveLength(1)

    await radio(group, "Ukulele").click()
    await expect(radio(group, "Ukulele")).toBeChecked()
    const level = group.getByRole("slider")
    await level.focus()
    await level.press("ArrowRight")
    await level.press("ArrowRight")
    await expect(group.getByTestId("music-level")).toHaveText("+2 dB")
    await level.scrollIntoViewIfNeeded()
    await shot(page, "studio-music-look")

    await dialog.getByRole("button", { name: "Make video" }).click()
    await expect(page).toHaveURL(new RegExp(`/studio/videos/${videoId}$`))
    expect(server.made).toEqual([
      {
        take: takeId,
        captions: true,
        aspect: "9:16",
        look: { music: { track: "mus_00000000000000cc", gain_db: 2 } },
      },
    ])
    expect(server.samples).toHaveLength(1)
  })

  // Scenario: the library is empty. The owner adds a file: it is sent as it is, named by the file, while the group
  // says it is uploading; once added it is picked. The route's refusals are shown in the server's words, or, from a
  // page that is not the server's (a proxy's 411), in the route's.
  test("a track is uploaded and picked, and a refused upload says why", async ({ page }) => {
    const server = await fakeServer(page)
    server.music = []
    server.usedBytes = 0
    server.defaultLook = savedLook
    const { group } = await openMake(page)

    await expect(group.getByTestId("music-empty")).toContainText("No tracks yet")
    await expect(radio(group, "No music")).toBeChecked()
    await showGroup(group)
    await shot(page, "studio-music-empty")

    const file = group.getByLabel("Music file")
    server.uploadAnswer = () => ({ delay: 1500 })
    await file.setInputFiles({ name: "Nhạc nền.mp3", mimeType: "audio/mpeg", buffer: mp3() })
    await expect(group.getByText("Uploading “Nhạc nền”")).toBeVisible()
    await expect(group.getByRole("button", { name: "Add music…" })).toBeDisabled()
    await shot(page, "studio-music-uploading")
    await expect(radio(group, "Nhạc nền")).toBeChecked()
    expect(server.uploads).toEqual([
      { name: "Nhạc nền", type: "application/octet-stream", bytes: mp3().length },
    ])

    const refusals: [number, string, RegExp][] = [
      [413, `{"code":"request_too_large","message":"a track is at most 64 MB"}`, /at most 64 MB/],
      [429, `{"code":"rate_limited","message":"another track is being uploaded"}`, /another track/],
      [411, "<html><body><h1>411 Length Required</h1></body></html>", /without its length/],
    ]
    for (const [status, body, line] of refusals) {
      server.uploadAnswer = () => ({ status, body })
      await file.setInputFiles({ name: "Dài.flac", mimeType: "audio/flac", buffer: flac() })
      const alert = group.getByRole("alert")
      await expect(alert).toContainText("“Dài” was not added")
      await expect(alert).toContainText(line)
      if (status === 413) {
        await alert.scrollIntoViewIfNeeded()
        await shot(page, "studio-music-refused")
      }
    }
    await expect(radio(group, "Nhạc nền")).toBeChecked()
  })

  // Scenario: the library holds nearly its 5 GB. An upload the quota cannot hold is refused before it is read (507),
  // in the server's words.
  test("an upload past the quota is refused with the server's reason", async ({ page }) => {
    const server = await fakeServer(page)
    server.usedBytes = 4_995_000_000
    server.uploadAnswer = () => ({
      status: 507,
      body: `{"code":"insufficient_storage","message":"too little room for the track: the library holds 4995 MB of its 5000 MB; this track is 12 MB"}`,
    })
    const { group } = await openMake(page)

    await expect(group.getByTestId("music-usage")).toHaveText("4.99 GB of 5 GB used")
    await group
      .getByLabel("Music file")
      .setInputFiles({ name: "Piano dài.wav", mimeType: "audio/wav", buffer: wav() })
    const alert = group.getByRole("alert")
    await expect(alert).toContainText("“Piano dài” was not added")
    await expect(alert).toContainText("the library holds 4995 MB of its 5000 MB")
    await showGroup(group)
    await shot(page, "studio-music-quota-full")
  })

  // Scenario: revision 1 was made with a track the owner deleted since (row 16). Its Change look keeps the track in
  // the list as deleted and says what the server will do; choosing another sends it, and the deleted one, which can
  // no longer be chosen, leaves the list. A revision made after the delete fell back to no music, which its Change
  // look says, as its review shows the server's warning.
  test("a deleted track is shown as deleted, and a look that fell back says so", async ({
    page,
  }) => {
    const server = await fakeServer(page)
    const gone = { track: "mus_00000000000de1e7", name: "Mưa", length_ms: 40_000 }
    const base = revision(1, "review").look
    server.revisions = [
      revision(1, "review", { look: { ...base, music: { ...gone, gain_db: -4 } } }),
    ]
    await page.goto(`/studio/videos/${videoId}`)

    const review = page.getByRole("complementary", { name: "Review" })
    await review.getByRole("button", { name: "Change look" }).click()
    const dialog = page.getByRole("dialog", { name: "Change the look" })
    const group = dialog.getByRole("group", { name: "Music" })
    await expect(radio(group, "Mưa \\(deleted\\)")).toBeChecked()
    await expect(group).toContainText("This track was deleted.")
    await expect(group.getByTestId("music-level")).toHaveText("-4 dB")
    await showGroup(group)
    await shot(page, "studio-music-deleted")

    await radio(group, "Lo-fi buổi sáng").click()
    await expect(radio(group, "Mưa \\(deleted\\)")).toHaveCount(0)
    await dialog.getByRole("button", { name: "Change look" }).click()
    await expect(dialog).toBeHidden()
    expect(server.looks[0]!.body).toEqual({ look: { music: { track: lofi, gain_db: -4 } } })

    server.revisions = [
      revision(1, "review", {
        look: { ...base, music: { track: "none", gain_db: 0, fell_back_from: gone } },
        warnings: [`the music "Mưa" was deleted, so the video has no music`],
      }),
    ]
    await page.goto(`/studio/videos/${videoId}`)
    await expect(review).toContainText(`the music "Mưa" was deleted, so the video has no music`)
    await review.getByRole("button", { name: "Change look" }).click()
    await expect(group.getByTestId("music-fell-back")).toHaveText(
      "“Mưa” was deleted, so this look was made without music.",
    )
    await expect(radio(group, "No music")).toBeChecked()
    await expect(radio(group, "Mưa")).toHaveCount(0)
    await showGroup(group)
    await shot(page, "studio-music-fell-back")
  })
})
