import { expect, test, type Page } from "@playwright/test"

import { bigLog, chart, docx, markdown, orders, reportPdf, sheets, svg } from "./fixtures/files.js"
import { shot } from "./shot.js"

// Files of every kind in the Files panel (issue #422), built, against a fake client API: a file that is not text
// opens in the app's file preview, read through /raw (a workbook through /sheets); text stays in the editor, a CSV
// with its table, a Markdown file with its preview.

const at = "2026-11-14T10:00:00Z"
const chat = {
  id: "cht_1",
  title: "Q3 report",
  bot: "reporter",
  working: false,
  coordinator: true,
  created_at: at,
  updated_at: at,
}
const workspace = {
  id: "bot",
  kind: "bot",
  name: "reporter",
  root: "/home/hyp/bots/reporter",
  online: true,
}

const binary = new Map<string, Buffer>([
  ["out/tai-chinh-q3.pdf", reportPdf],
  ["out/chart.png", chart()],
  ["out/flow.svg", svg],
  ["out/revenue.xlsx", Buffer.from("PK\u0003\u0004")],
  ["out/bao-cao-q3.docx", docx],
  ["out/slides.pptx", Buffer.from("PK\u0003\u0004")],
])
const text = new Map<string, string>([
  ["out/orders.csv", orders],
  ["out/summary.md", markdown],
  ["out/worker.log", bigLog],
])

async function fakeServer(page: Page) {
  const asked: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const ws = /^\/chats\/cht_1\/workspaces\/bot\/(tree|file|raw|sheets|changes)$/.exec(path)
    if (ws) {
      const op = ws[1]!
      const where = url.searchParams.get("path") ?? ""
      asked.push(`${op} ${where}`)

      switch (op) {
        case "changes":
          return json({ git: false, changes: [], truncated: false })
        case "tree": {
          const names = where === "" ? ["out"] : [...binary.keys(), ...text.keys()]
          const entries = names.map((p) => ({
            name: p.slice(p.lastIndexOf("/") + 1),
            path: p,
            type: p.includes("/") ? "file" : "directory",
            bytes: 1,
            modified_at: 1,
          }))

          return json({ entries, has_more: false })
        }
        case "sheets":
          return json(sheets)
        case "raw": {
          const bytes = binary.get(where) ?? Buffer.from(text.get(where) ?? "")

          return route.fulfill({
            status: 200,
            contentType: "application/octet-stream",
            headers: { "Content-Disposition": `attachment; filename=${where.split("/").pop()}` },
            body: bytes,
          })
        }
        default: {
          if (binary.has(where))
            return json({ path: where, bytes: 100, binary: true, truncated: false, content: "" })
          const content = text.get(where) ?? ""

          return json({
            path: where,
            bytes: content.length,
            binary: false,
            truncated: false,
            content,
            sha256: `${where}@1`,
          })
        }
      }
    }

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/chats":
        return json({ data: [chat] })
      case "/chats/cht_1":
        return json(chat)
      case "/chats/cht_1/workspaces":
        return json({ data: [workspace] })
      case "/chats/cht_1/stream":
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return asked
}

const panel = (page: Page) => page.getByRole("complementary", { name: "Files" })

// open opens a file of out/ from the panel's All files; on a phone the panel gives way to the file it opened.
async function open(page: Page, name: string) {
  const files = page.getByRole("button", { name: "Files", exact: true })
  if ((await files.getAttribute("aria-pressed")) !== "true") await files.click()
  await expect(panel(page)).toBeVisible()
  const all = panel(page).getByRole("button", { name: "All files" })
  if ((await all.getAttribute("aria-pressed")) !== "true") await all.click()
  const tree = panel(page).getByRole("tree", { name: "All files" })
  const out = tree.getByRole("treeitem", { name: "out", exact: true })
  if ((await out.getAttribute("aria-expanded")) !== "true") await out.click()
  await tree.getByRole("treeitem", { name, exact: true }).click()
  await expect(page.getByRole("tab", { name }).last()).toHaveAttribute("aria-selected", "true")
}

// The page: a tab behind keeps its body, hidden, which a role and :visible pass over.
const body = (page: Page) => page.locator("body")

// Scenario: the owner opens what a Bot made in its folder, one file of each kind. A PDF is drawn page by page, a
// picture from its bytes, an SVG in a locked frame from its text (the folder has no sandboxed address, and a blob:
// address would let its script run if opened on its own), a workbook as a table a sheet, a Word document as its
// pages; a CSV and a Markdown file show as they read and stay editable as their source; a log past 1 MiB stays in
// the editor; a deck is a download.
test("each kind of file in a folder opens in its own view", async ({ page }) => {
  const asked = await fakeServer(page)
  await page.goto("/chats/cht_1")

  await open(page, "tai-chinh-q3.pdf")
  await expect(body(page).locator('figure[data-page="1"] canvas')).toBeVisible()
  await expect(body(page).getByRole("spinbutton", { name: "Page number" })).toHaveValue("1")
  await expect(body(page).getByRole("link", { name: "Download" })).toHaveAttribute(
    "href",
    "/api/v1/chats/cht_1/workspaces/bot/raw?path=out%2Ftai-chinh-q3.pdf",
  )
  await shot(page, "files-pdf")

  await open(page, "chart.png")
  const png = body(page).getByRole("img", { name: "out/chart.png" })
  await expect(png).toBeVisible()
  expect(await png.getAttribute("src")).toMatch(/^blob:/)
  await shot(page, "files-image")

  await open(page, "flow.svg")
  const frame = body(page).locator('iframe[title="out/flow.svg"]')
  await expect(frame).toHaveAttribute("sandbox", "")
  await expect(frame.contentFrame().getByRole("img", { name: "out/flow.svg" })).toBeVisible()
  expect(await frame.contentFrame().locator("img").getAttribute("src")).toMatch(
    /^data:image\/svg\+xml,/,
  )
  await expect(page.locator('img[src^="blob:"][alt="out/flow.svg"]')).toHaveCount(0)
  expect(await page.title()).not.toBe("pwned")
  await shot(page, "files-svg")

  await open(page, "revenue.xlsx")
  await expect(body(page).getByRole("tab", { name: "Revenue Q3" })).toBeVisible()
  await expect(body(page).getByRole("cell", { name: "TP.HCM" })).toBeVisible()
  await shot(page, "files-xlsx")
  await body(page).getByRole("tab", { name: "Daily log" }).click()
  await expect(body(page).getByRole("note")).toContainText("Only the first 1,000 rows are shown.")
  expect(asked).toContain("sheets out/revenue.xlsx")

  await open(page, "bao-cao-q3.docx")
  await expect(body(page).locator('[data-slot="docx-preview"] section.docx').first()).toBeVisible()
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
  await shot(page, "files-docx")

  await open(page, "orders.csv")
  await expect(
    body(page).getByRole("cell", { name: "45 Trần Phú, Hải Châu, Đà Nẵng" }),
  ).toBeVisible()
  await shot(page, "files-csv")
  // Its source is still the owner's to change.
  await body(page).getByRole("button", { name: "Show the source" }).click()
  await expect(body(page).locator('[data-testid="code-view"]:visible .view-lines')).toContainText(
    "DH-0001",
  )
  await body(page).getByRole("button", { name: "Show the table" }).click()
  await expect(body(page).getByRole("columnheader", { name: "Customer" })).toBeVisible()

  await open(page, "summary.md")
  await expect(body(page).getByRole("heading", { name: "Q3 summary" })).toBeVisible()
  await shot(page, "files-markdown")

  await open(page, "worker.log")
  await expect(body(page)).toContainText("Read only: larger than 1 MiB")
  await expect(body(page).locator('[data-testid="code-view"]:visible .view-lines')).toContainText(
    "handled request 0",
  )
  await shot(page, "files-large-text")

  await open(page, "slides.pptx")
  await expect(
    body(page).getByRole("link", { name: "Download", exact: true }).last(),
  ).toHaveAttribute("href", "/api/v1/chats/cht_1/workspaces/bot/raw?path=out%2Fslides.pptx")
  await expect(body(page)).toContainText("This kind of file is not shown here.")
  await shot(page, "files-download")
  // A file the preview cannot show is never read to find that out.
  expect(asked).not.toContain("raw out/slides.pptx")
})
