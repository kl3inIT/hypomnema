import { crc32, deflateSync } from "node:zlib"

import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Artifacts (issue #402) against a fake client API: a Bot's dashboard, PDF, image and spreadsheet in a chat, each a
// card; a page in its locked frame, its data from the frame's own address; its revisions, Code, full screen and
// its own page in a new tab. The chat's stream is held open by the test, which writes frames into it, as in
// chat.spec.ts.

const at = "2026-11-14T09:00:00Z"
const chat = {
  id: "cht_1",
  title: "Sales dashboard",
  bot: "reporter",
  working: false,
  coordinator: true,
  created_at: at,
  updated_at: at,
}

// A dashboard as a Bot writes one: its own styles and script, its figures read from data.json beside it.
const dashboard = (week: number, regions: boolean) => `<!doctype html>
<html><head><meta charset="utf-8"><title>Sales</title>
<style>
body{font:14px system-ui,sans-serif;margin:0;padding:20px;color:#1f2330;background:#fff}
h1{font-size:20px;margin:0 0 16px}.kpis{display:grid;grid-template-columns:repeat(3,1fr);gap:12px;margin-bottom:16px}
.kpi{border:1px solid #e3e5ec;border-radius:10px;padding:12px}.kpi b{display:block;font-size:22px;margin-top:4px}
.kpi small{color:#2f9e5b}.chart{border:1px solid #e3e5ec;border-radius:10px;padding:12px}
.bars{display:flex;align-items:flex-end;gap:10px;height:160px;margin-top:10px}
.bar{flex:1;background:#5b5bd6;border-radius:4px 4px 0 0;position:relative}
.bar span{position:absolute;bottom:-18px;left:0;right:0;text-align:center;font-size:11px;color:#6b7080}
</style></head>
<body><h1>Sales, week ${week}</h1>
<div class="kpis"><div class="kpi">Revenue<b id="revenue">…</b><small>▲ 12% on week ${week - 1}</small></div>
<div class="kpi">Orders<b>3,410</b><small>▲ 5%</small></div><div class="kpi">Per order<b>364k</b><small>▲ 7%</small></div></div>
<div class="chart">By day<div class="bars" id="bars"></div></div>
${regions ? '<div class="chart" style="margin-top:12px">By region: Hà Nội 46%, TP.HCM 38%, Đà Nẵng 10%</div>' : ""}
<script>
fetch("data.json").then(r => r.json()).then(d => {
  document.getElementById("revenue").textContent = d.revenue
  document.getElementById("bars").innerHTML = d.days.map(v =>
    '<div class="bar" style="height:' + v + '%"><span>' + v + '</span></div>').join("")
})
</script></body></html>`

const data = JSON.stringify({ revenue: "1,24 tỷ", days: [62, 66, 58, 74, 81, 90, 54] })

// pdf is a one-page PDF that says its text, with a cross-reference table that is right.
function pdf(text: string) {
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
    `<< /Length ${44 + text.length} >>\nstream\nBT /F1 24 Tf 72 760 Td (${text}) Tj ET\nendstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
  ]
  let body = "%PDF-1.4\n"
  const offsets: number[] = []
  objects.forEach((o, i) => {
    offsets.push(body.length)
    body += `${i + 1} 0 obj\n${o}\nendobj\n`
  })
  const xref = body.length
  body += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`
  for (const o of offsets) body += `${String(o).padStart(10, "0")} 00000 n \n`
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`

  return Buffer.from(body, "latin1")
}

// A small chart as a PNG: five bars on white.
function chart() {
  const width = 240
  const height = 120
  const heights = [40, 70, 55, 90, 105]
  const rows: number[] = []
  for (let y = 0; y < height; y++) {
    rows.push(0)
    for (let x = 0; x < width; x++) {
      const bar = Math.floor(x / 48)
      const inBar = x % 48 > 8 && x % 48 < 40 && height - y <= (heights[bar] ?? 0)
      rows.push(...(inBar ? [91, 91, 214] : [255, 255, 255]))
    }
  }
  const chunk = (type: string, body: Buffer) => {
    const head = Buffer.alloc(4)
    head.writeUInt32BE(body.length)
    const typed = Buffer.concat([Buffer.from(type, "latin1"), body])
    const crc = Buffer.alloc(4)
    crc.writeUInt32BE(crc32(typed))

    return Buffer.concat([head, typed, crc])
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width, 0)
  header.writeUInt32BE(height, 4)
  header.set([8, 2, 0, 0, 0], 8)

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(Buffer.from(rows))),
    chunk("IEND", Buffer.alloc(0)),
  ])
}

const png = chart()

type Rev = {
  revision: number
  title: string
  type: string
  entry: string
  files: { path: string; bytes: Buffer; media_type: string }[]
  created_at: string
}

type Kept = { id: string; bot: string; revisions: Rev[] }

const page1: Rev = {
  revision: 1,
  title: "Sales, week 46",
  type: "html",
  entry: "index.html",
  files: [
    { path: "index.html", bytes: Buffer.from(dashboard(46, false)), media_type: "text/html" },
    { path: "data.json", bytes: Buffer.from(data), media_type: "application/json" },
  ],
  created_at: "2026-11-14T08:50:00Z",
}
const page2: Rev = {
  ...page1,
  revision: 2,
  title: "Sales, week 46 by region",
  files: [
    { path: "index.html", bytes: Buffer.from(dashboard(46, true)), media_type: "text/html" },
    page1.files[1]!,
  ],
  created_at: "2026-11-14T08:58:00Z",
}

const one = (
  id: string,
  title: string,
  type: string,
  entry: string,
  bytes: Buffer,
  media: string,
): Kept => ({
  id,
  bot: "reporter",
  revisions: [
    {
      revision: 1,
      title,
      type,
      entry,
      files: [{ path: entry, bytes, media_type: media }],
      created_at: "2026-11-14T08:40:00Z",
    },
  ],
})

function world() {
  const artifacts: Record<string, Kept> = {
    art_page: { id: "art_page", bot: "reporter", revisions: [page1] },
    art_pdf: one(
      "art_pdf",
      "Quarterly report",
      "pdf",
      "report.pdf",
      pdf("Quarterly report Q3"),
      "application/pdf",
    ),
    art_img: one("art_img", "Revenue chart", "image", "chart.png", png, "image/png"),
    art_xls: one(
      "art_xls",
      "Orders, October",
      "file",
      "orders.xlsx",
      Buffer.from("PK\u0003\u0004 a sheet"),
      "application/octet-stream",
    ),
  }

  const toJSON = (k: Kept) => {
    const revs = [...k.revisions].reverse()
    const newest = revs[0]!

    return {
      id: k.id,
      chat_id: chat.id,
      bot: k.bot,
      revision: newest.revision,
      title: newest.title,
      type: newest.type,
      entry: newest.entry,
      size: newest.files.reduce((n, f) => n + f.bytes.length, 0),
      created_at: k.revisions[0]!.created_at,
      updated_at: newest.created_at,
      revisions: revs.map((r) => ({
        revision: r.revision,
        title: r.title,
        type: r.type,
        entry: r.entry,
        size: r.files.reduce((n, f) => n + f.bytes.length, 0),
        files: r.files.map((f) => ({
          path: f.path,
          size: f.bytes.length,
          media_type: f.media_type,
        })),
        created_at: r.created_at,
      })),
    }
  }

  const card = (k: Kept) => {
    const a = toJSON(k)

    return {
      id: a.id,
      revision: a.revision,
      title: a.title,
      type: a.type,
      updated_at: a.updated_at,
    }
  }

  return { artifacts, toJSON, card }
}

type Streams = { push: (text: string) => void }[]
type PageGlobal = { fetch: typeof fetch; streams: Streams }

const frame = (seq: number, m: object, id = true) =>
  `${id ? `id: ${seq}\n` : ""}event: message\ndata: ${JSON.stringify({ seq, chat_id: chat.id, created_at: at, ...m })}\n\n`

async function fakeServer(page: Page, w: ReturnType<typeof world>) {
  const frames: string[] = []

  await page.addInitScript(() => {
    const g = globalThis as unknown as PageGlobal
    const streams: Streams = []
    g.streams = streams
    const real = g.fetch.bind(globalThis)

    g.fetch = (input, init) => {
      const url = input instanceof Request ? input.url : String(input)
      if (!/\/chats\/[^/]+\/stream/.test(url)) return real(input, init)

      const encoder = new TextEncoder()
      let writer: ReadableStreamDefaultController<Uint8Array> | undefined
      const body = new ReadableStream<Uint8Array>({ start: (c) => void (writer = c) })
      streams.push({ push: (text) => writer?.enqueue(encoder.encode(text)) })

      return Promise.resolve(
        new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
      )
    }
  })

  // On the context, so that a tab the page opens has the same server.
  await page.context().route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    // A folder page's files, as the server sends them to the locked frame: sandboxed, readable from its opaque
    // origin.
    const files = /^\/artifact-files\/tok-(\w+)-(\d+)\/(.+)$/.exec(path)
    if (files) {
      const [, id, rev, name] = files
      const f = w.artifacts[`art_${id}`]?.revisions
        .find((r) => r.revision === Number(rev))
        ?.files.find((x) => x.path === name)
      if (!f) return route.fulfill({ status: 404, body: "Not found." })

      return route.fulfill({
        status: 200,
        contentType: f.media_type,
        headers: {
          "Access-Control-Allow-Origin": "*",
          "Content-Security-Policy": "sandbox allow-scripts; default-src 'none'",
        },
        body: f.bytes,
      })
    }

    const one = /^\/artifacts\/(art_\w+)(?:\/revisions\/(\d+)\/(file|frame|archive))?$/.exec(path)
    if (one) {
      const [, id, rev, op] = one
      const k = w.artifacts[id!]
      if (!k) return json({ code: "not_found", message: "not found" }, 404)
      if (!op) return json(w.toJSON(k))

      const r = k.revisions.find((x) => x.revision === Number(rev))!
      if (op === "frame") return json({ url: `/api/v1/artifact-files/tok-${id!.slice(4)}-${rev}/` })

      const name = url.searchParams.get("path") ?? r.entry
      const f = r.files.find((x) => x.path === name)!

      return route.fulfill({
        status: 200,
        contentType: "application/octet-stream",
        headers: { "Content-Disposition": `attachment; filename=${f.path}` },
        body: f.bytes,
      })
    }

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/chats":
        return json({ data: [chat] })
      case `/chats/${chat.id}`:
        return json(chat)
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "/bots":
        return json({ data: [] })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  const push = async (text: string) => {
    frames.push(text)
    await page.waitForFunction(() => (globalThis as unknown as PageGlobal).streams.length > 0)
    await page.evaluate((t) => {
      for (const s of (globalThis as unknown as PageGlobal).streams) s.push(t)
    }, text)
  }

  return { push }
}

// The conversation: the owner asks, the Bot shows the dashboard and says so, then a PDF, an image and a sheet.
async function openChat(page: Page, w: ReturnType<typeof world>) {
  const server = await fakeServer(page, w)
  await page.goto(`/chats/${chat.id}`)

  await server.push(`event: state\ndata: {"working":false}\n\n`)
  await server.push(frame(1, { role: "owner", text: "Make me a dashboard of this week's sales" }))
  await server.push(
    frame(2, {
      role: "system",
      kind: "artifact",
      text: "Sales",
      artifact: w.card(w.artifacts.art_page!),
    }),
  )
  await server.push(frame(3, { role: "coordinator", text: "Here is week 46: revenue is up 12%." }))
  await server.push(frame(4, { role: "owner", text: "And the report, the chart and the orders?" }))
  for (const [seq, id] of [
    [5, "art_pdf"],
    [6, "art_img"],
    [7, "art_xls"],
  ] as const) {
    await server.push(
      frame(seq, {
        role: "system",
        kind: "artifact",
        text: "",
        artifact: w.card(w.artifacts[id]!),
      }),
    )
  }

  return server
}

const card = (page: Page, title: string) => page.getByRole("button", { name: `Open ${title}` })
const side = (page: Page) => page.getByRole("complementary", { name: "Artifact" })

test.describe("on a desktop", () => {
  test.skip(({ isMobile }) => isMobile, "the panel beside the chat is a desktop's")

  // Scenario: the owner opens the dashboard beside the chat. It runs in a locked frame (scripts, no same origin, no
  // referrer) and reads its figures from the frame's own address. The Bot revises it: the card says v2 where it
  // was, and the panel follows; the owner steps back to v1 and forward again, reads its Code, goes full screen and
  // back with Esc, and opens it on its own page in a new tab, which goes back to the chat.
  test("a dashboard opens beside the chat, with its revisions, code, full screen and own page", async ({
    page,
    context,
  }) => {
    const w = world()
    const server = await openChat(page, w)

    await expect(card(page, "Sales, week 46")).toContainText("v1")
    await shot(page, "artifact-card")

    await card(page, "Sales, week 46").click()
    await expect(side(page)).toContainText("Sales, week 46")
    const iframe = side(page).locator("iframe")
    await expect(iframe).toHaveAttribute("sandbox", "allow-scripts allow-forms")
    await expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer")
    const inFrame = side(page).frameLocator("iframe")
    await expect(inFrame.getByRole("heading", { name: "Sales, week 46" })).toBeVisible()
    await expect(inFrame.locator("#revenue")).toHaveText("1,24 tỷ")
    // The page's origin is opaque: it cannot read the app's storage or cookies.
    expect(await iframe.evaluate((f: { contentDocument: unknown }) => f.contentDocument)).toBeNull()
    await shot(page, "artifact-panel")

    // The Bot shows its next revision; the stream sends the message again.
    w.artifacts.art_page!.revisions.push(page2)
    await server.push(
      frame(
        2,
        { role: "system", kind: "artifact", text: "", artifact: w.card(w.artifacts.art_page!) },
        false,
      ),
    )
    await expect(card(page, "Sales, week 46 by region")).toContainText("v2")
    await expect(side(page).frameLocator("iframe").getByText("By region")).toBeVisible()
    await expect(side(page)).toContainText("v2 · Updated")
    await shot(page, "artifact-revision-2")

    await side(page).getByRole("button", { name: "Older revision" }).click()
    await expect(side(page)).toContainText("v1 of 2")
    await expect(
      side(page).frameLocator("iframe").getByRole("heading", { name: "Sales, week 46" }),
    ).toBeVisible()
    await expect(side(page).frameLocator("iframe").getByText("By region")).toHaveCount(0)
    await shot(page, "artifact-revision-1")
    await side(page).getByRole("button", { name: "Newer revision" }).click()
    await expect(side(page).frameLocator("iframe").getByText("By region")).toBeVisible()

    await side(page).getByRole("button", { name: "Code", exact: true }).click()
    await expect(side(page).locator('[data-testid="code-view"] .view-lines')).toContainText(
      "By region",
    )
    await shot(page, "artifact-code")
    await side(page).getByRole("button", { name: "Preview", exact: true }).click()

    await side(page).getByRole("button", { name: "Full screen" }).click()
    const full = page.getByRole("dialog", { name: "Artifact, full screen" })
    await expect(full.frameLocator("iframe").getByText("By region")).toBeVisible()
    await shot(page, "artifact-full-screen")
    await page.keyboard.press("Escape")
    await expect(full).toHaveCount(0)
    await expect(side(page)).toBeVisible()

    const opened = context.waitForEvent("page")
    await side(page).getByRole("button", { name: "Open in a new tab" }).click()
    const tab = await opened
    await tab.waitForLoadState()
    expect(new URL(tab.url()).pathname).toBe("/artifacts/art_page")
    await expect(tab.getByText("Sales, week 46 by region")).toBeVisible()
    await expect(tab.getByText("by reporter")).toBeVisible()
    await expect(tab.frameLocator("iframe").locator("#revenue")).toHaveText("1,24 tỷ")
    await shot(tab, "artifact-new-tab")
    await tab.getByLabel("Back to the chat").click()
    await expect(tab).toHaveURL(/\/chats\/cht_1$/)

    await side(page).getByRole("button", { name: "Close" }).click()
    await expect(side(page)).toHaveCount(0)
  })

  // Scenario: a PDF is drawn page by page in the app, an image is shown from its bytes and zooms, and a sheet the
  // app has no view for is offered to download, never opened as a page.
  test("a PDF, an image and a file each have their view", async ({ page }) => {
    const w = world()
    await openChat(page, w)

    await card(page, "Quarterly report").click()
    await expect(side(page)).toContainText("1 page")
    await expect(side(page).locator(".react-pdf__Page canvas")).toBeVisible()
    await expect(side(page).getByText("Quarterly report Q3")).toBeAttached()
    await shot(page, "artifact-pdf")

    await card(page, "Revenue chart").click()
    await expect(side(page).getByRole("img", { name: "Revenue chart" })).toBeVisible()
    await side(page).getByRole("button", { name: "Zoom in" }).click()
    await expect(side(page).getByRole("button", { name: "Reset zoom" })).toHaveText("125%")
    await shot(page, "artifact-image")

    await card(page, "Orders, October").click()
    const download = side(page).getByRole("link", { name: "Download" })
    await expect(download).toHaveAttribute("href", "/api/v1/artifacts/art_xls/revisions/1/file")
    await expect(side(page)).toContainText("orders.xlsx")
    await shot(page, "artifact-file")
  })
})

test.describe("on a phone", () => {
  test.skip(({ isMobile }) => !isMobile, "a phone has no room beside the chat")

  // Scenario: on a phone a card opens its artifact over the whole screen, with its new tab; closing it is back in
  // the chat. A PDF, an image and a file open the same way.
  test("a card opens its artifact over the whole screen", async ({ page }) => {
    const w = world()
    await openChat(page, w)

    await card(page, "Sales, week 46").scrollIntoViewIfNeeded()
    await shot(page, "artifact-card")

    await card(page, "Sales, week 46").click()
    const full = page.getByRole("dialog", { name: "Artifact, full screen" })
    await expect(full.frameLocator("iframe").locator("#revenue")).toHaveText("1,24 tỷ")
    await expect(full.getByRole("button", { name: "Open in a new tab" })).toBeVisible()
    await expect(full.getByRole("button", { name: "Full screen" })).toHaveCount(0)
    await shot(page, "artifact-panel")
    await full.getByRole("button", { name: "Close" }).click()
    await expect(full).toHaveCount(0)

    for (const [title, name] of [
      ["Quarterly report", "artifact-pdf"],
      ["Revenue chart", "artifact-image"],
      ["Orders, October", "artifact-file"],
    ] as const) {
      await card(page, title).click()
      const shown = page.getByRole("dialog", { name: "Artifact, full screen" })
      await expect(shown).toContainText(title)
      if (title === "Quarterly report") await expect(shown).toContainText("1 page")
      await shot(page, name)
      await shown.getByRole("button", { name: "Close" }).click()
    }
  })

  test("an artifact's own page fits a phone", async ({ page }) => {
    const w = world()
    w.artifacts.art_page!.revisions.push(page2)
    await fakeServer(page, w)
    await page.goto("/artifacts/art_page?rev=1")

    await expect(page.getByText("v1 of 2")).toBeVisible()
    await expect(
      page.frameLocator("iframe").getByRole("heading", { name: "Sales, week 46" }),
    ).toBeVisible()
    await shot(page, "artifact-new-tab")
  })
})
