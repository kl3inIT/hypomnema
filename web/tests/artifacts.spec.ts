import { expect, test, type Page } from "@playwright/test"

import {
  bigLog,
  chart,
  docx,
  markdown,
  orders,
  pdf,
  python,
  reportPdf,
  sheets,
  svg,
} from "./fixtures/files.js"
import { shot } from "./shot.js"

// Artifacts (issues #402 and #422) against a fake client API: a Bot's dashboard and files of every kind in a chat,
// each a card; a page in its locked frame, its data from the frame's own address; its revisions, Code, full screen and
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

const png = chart()
const octet = "application/octet-stream"

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
    art_svg: one("art_svg", "Flow", "image", "flow.svg", svg, "image/svg+xml"),
    art_xls: one(
      "art_xls",
      "Orders, October",
      "file",
      "orders.xlsx",
      Buffer.from("PK\u0003\u0004 a sheet"),
      "application/octet-stream",
    ),
    art_csv: one("art_csv", "Order list", "file", "orders.csv", Buffer.from(orders), octet),
    art_doc: one("art_doc", "Q3 report", "file", "bao-cao-q3.docx", docx, octet),
    art_md: one("art_md", "Q3 summary", "file", "summary.md", Buffer.from(markdown), octet),
    art_py: one("art_py", "Revenue script", "file", "revenue.py", Buffer.from(python), octet),
    art_log: one("art_log", "Worker log", "file", "worker.log", Buffer.from(bigLog), octet),
    art_ppt: one(
      "art_ppt",
      "Q3 slides",
      "file",
      "slides.pptx",
      Buffer.from("PK\u0003\u0004"),
      octet,
    ),
    art_fin: one("art_fin", "Finance Q3", "pdf", "tai-chinh-q3.pdf", reportPdf, "application/pdf"),
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

    const one = /^\/artifacts\/(art_\w+)(?:\/revisions\/(\d+)\/(file|frame|archive|sheets))?$/.exec(
      path,
    )
    if (one) {
      const [, id, rev, op] = one
      const k = w.artifacts[id!]
      if (!k) return json({ code: "not_found", message: "not found" }, 404)
      if (!op) return json(w.toJSON(k))

      const r = k.revisions.find((x) => x.revision === Number(rev))!
      if (op === "frame") return json({ url: `/api/v1/artifact-files/tok-${id!.slice(4)}-${rev}/` })
      // A workbook as the control plane reads it (internal/controlplane/spreadsheet).
      if (op === "sheets") return json(sheets)

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
    [8, "art_svg"],
    [9, "art_csv"],
    [10, "art_doc"],
    [11, "art_md"],
    [12, "art_py"],
    [13, "art_log"],
    [14, "art_ppt"],
    [15, "art_fin"],
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

  // Scenario: each kind of file a Bot hands over opens in the panel in its own view (issue #422): a PDF drawn page
  // by page with its page field and zoom, an image from its bytes that zooms, an SVG from the frame's sandboxed
  // address, a workbook as a table a sheet, a CSV as a table, a Word document as its pages, Markdown as a document,
  // source in the editor, a large log as its first megabyte with the download, and a deck as a download.
  test("every kind of file has its view beside the chat", async ({ page }) => {
    const w = world()
    await openChat(page, w)

    await card(page, "Quarterly report").click()
    const toolbar = side(page).getByRole("toolbar", { name: "Document controls" })
    await expect(toolbar.getByRole("spinbutton", { name: "Page number" })).toHaveValue("1")
    await expect(toolbar).toContainText("/ 1")
    await expect(side(page).locator('figure[data-page="1"] canvas')).toBeVisible()
    await toolbar.getByRole("button", { name: "Zoom in" }).click()
    await expect(toolbar).toContainText("150%")
    await shot(page, "artifact-pdf")

    await card(page, "Revenue chart").click()
    await expect(side(page).getByRole("img", { name: "chart.png" })).toBeVisible()
    await side(page).getByRole("button", { name: "Zoom in" }).click()
    await expect(side(page).getByText("125%")).toBeVisible()
    await shot(page, "artifact-image")

    // A raster image is drawn from its bytes; an SVG never from a blob: address of this origin, where its script
    // would run if it were opened on its own, but from the frame's sandboxed address.
    expect(await side(page).getByRole("img", { name: "chart.png" }).getAttribute("src")).toMatch(
      /^blob:/,
    )
    await card(page, "Flow").click()
    const flow = side(page).getByRole("img", { name: "flow.svg" })
    await expect(flow).toBeVisible()
    expect(await flow.getAttribute("src")).toMatch(
      /^http:\/\/127\.0\.0\.1:4173\/api\/v1\/artifact-files\/tok-svg-1\/flow\.svg$/,
    )
    await expect(page.locator('img[src^="blob:"][alt="flow.svg"]')).toHaveCount(0)
    expect(await page.title()).not.toBe("pwned")
    await shot(page, "artifact-svg")

    await card(page, "Orders, October").click()
    await expect(side(page).getByRole("tab", { name: "Revenue Q3" })).toBeVisible()
    await expect(side(page).getByRole("cell", { name: "Hà Nội", exact: true })).toBeVisible()
    await shot(page, "artifact-xlsx")
    await side(page).getByRole("tab", { name: "Daily log" }).click()
    await expect(side(page).getByRole("note")).toContainText("Only the first 1,000 rows are shown.")
    await expect(side(page).getByRole("link", { name: "Download the whole file" })).toHaveAttribute(
      "href",
      "/api/v1/artifacts/art_xls/revisions/1/file",
    )
    await shot(page, "artifact-xlsx-cut")

    await card(page, "Order list").click()
    await expect(side(page).getByRole("cell", { name: "12 Lê Lợi, Quận 1, TP.HCM" })).toBeVisible()
    await expect(side(page).getByRole("columnheader", { name: "Customer" })).toBeVisible()
    await shot(page, "artifact-csv")

    await card(page, "Q3 report").click()
    await expect(
      side(page).locator('[data-slot="docx-preview"] section.docx').first(),
    ).toBeVisible()
    await expect(side(page).locator('[data-slot="docx-preview"] script')).toHaveCount(0)

    // The document's styles stay inside its shadow root: none of them reach the app around it.
    expect(
      await page.evaluate(() => {
        type Sheets = { adoptedStyleSheets: { cssRules: ArrayLike<{ cssText: string }> }[] }
        const doc = (globalThis as unknown as { document: Sheets }).document

        return doc.adoptedStyleSheets.some((s) =>
          Array.from(s.cssRules).some((r) => r.cssText.includes("docx")),
        )
      }),
    ).toBe(false)
    expect(
      await page
        .locator('[data-slot="docx-preview"]:visible')
        .evaluate((h) => h.shadowRoot?.adoptedStyleSheets.length ?? 0),
    ).toBeGreaterThan(1)
    await shot(page, "artifact-docx")

    await card(page, "Q3 summary").click()
    await expect(side(page).getByRole("heading", { name: "Q3 summary" })).toBeVisible()
    await expect(side(page).getByRole("cell", { name: "1,299" })).toBeVisible()
    await shot(page, "artifact-markdown")

    await card(page, "Revenue script").click()
    await expect(side(page).locator('[data-testid="code-view"] .view-lines')).toContainText(
      "groupby",
    )
    await shot(page, "artifact-code")

    await card(page, "Worker log").click()
    await expect(side(page).getByRole("note")).toContainText(
      "Only the first 1 MB of the file is shown.",
    )
    await expect(side(page).getByRole("link", { name: "Download the whole file" })).toHaveAttribute(
      "href",
      "/api/v1/artifacts/art_log/revisions/1/file",
    )
    await expect(side(page).locator('[data-testid="code-view"] .view-lines')).toContainText(
      "handled request 0",
    )
    await shot(page, "artifact-large-text")

    await card(page, "Q3 slides").click()
    await expect(side(page).getByRole("link", { name: "Download" })).toHaveAttribute(
      "href",
      "/api/v1/artifacts/art_ppt/revisions/1/file",
    )
    await expect(side(page)).toContainText("slides.pptx")
    await shot(page, "artifact-file")

    // Full screen has room for the page rail beside a PDF of several pages.
    await card(page, "Finance Q3").click()
    await side(page).getByRole("button", { name: "Full screen" }).click()
    const full = page.getByRole("dialog", { name: "Artifact, full screen" })
    const rail = full.getByRole("navigation", { name: "Pages" })
    await expect(rail.getByRole("button", { name: "Page 1" })).toHaveAttribute(
      "aria-current",
      "true",
    )
    await expect(full.locator('figure[data-page="1"] canvas')).toBeVisible()
    await rail.getByRole("button", { name: "Page 2" }).click()
    await expect(full.getByRole("spinbutton", { name: "Page number" })).toHaveValue("2")
    await shot(page, "artifact-pdf-full-screen")
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

    const shown = page.getByRole("dialog", { name: "Artifact, full screen" })
    const kinds: [string, string, (d: typeof shown) => Promise<void>][] = [
      [
        "Quarterly report",
        "artifact-pdf",
        (d) => expect(d.locator('figure[data-page="1"] canvas')).toBeVisible(),
      ],
      [
        "Revenue chart",
        "artifact-image",
        (d) => expect(d.getByRole("img", { name: "chart.png" })).toBeVisible(),
      ],
      [
        "Flow",
        "artifact-svg",
        (d) => expect(d.getByRole("img", { name: "flow.svg" })).toBeVisible(),
      ],
      [
        "Orders, October",
        "artifact-xlsx",
        (d) => expect(d.getByRole("cell", { name: "Hà Nội", exact: true })).toBeVisible(),
      ],
      [
        "Order list",
        "artifact-csv",
        (d) => expect(d.getByRole("columnheader", { name: "Customer" })).toBeVisible(),
      ],
      [
        "Q3 report",
        "artifact-docx",
        (d) => expect(d.locator('[data-slot="docx-preview"] section.docx').first()).toBeVisible(),
      ],
      [
        "Q3 summary",
        "artifact-markdown",
        (d) => expect(d.getByRole("heading", { name: "Q3 summary" })).toBeVisible(),
      ],
      [
        "Revenue script",
        "artifact-code",
        (d) => expect(d.locator('[data-testid="code-view"] .view-lines')).toContainText("groupby"),
      ],
      [
        "Worker log",
        "artifact-large-text",
        (d) => expect(d.getByRole("note")).toContainText("Only the first 1 MB"),
      ],
      [
        "Q3 slides",
        "artifact-file",
        (d) => expect(d.getByRole("link", { name: "Download" })).toBeVisible(),
      ],
    ]
    for (const [title, name, check] of kinds) {
      await card(page, title).click()
      await expect(shown.getByText(title, { exact: true }).first()).toBeVisible()
      await check(shown)
      await shot(page, name)
      await shown.getByRole("button", { name: "Close" }).click()
      await expect(shown).toHaveCount(0)
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
