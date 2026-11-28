import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Issue #1207: the owner hands an item to a Bot and waits for its result on the item. The Bot posts its export and
// the chart it showed in a comment: the file is the chat's chip that downloads it from the Bot's folder, the chart
// the chat's Artifact card, and the Artifact's id in the text a link named by its title. A file no longer in the
// folder turns its chip red with the server's message.

const at = (ms: number) => new Date(Date.now() + ms).toISOString()
const hour = 3_600_000

const chat = "cht_eag7jejewob7v65py6ydqip4wu"
const chart = "art_uhblefbe4k6v3pvvpvqml7gijq"
const page1 = `<!doctype html><html><head><meta charset="utf-8"></head><body style="font-family:sans-serif;margin:32px">
<h1 style="font-size:40px">Doanh số tháng 10</h1>
<div style="display:flex;gap:16px;align-items:flex-end;height:360px">
${[52, 70, 64, 88, 95, 120]
  .map(
    (h) =>
      `<div style="width:120px;height:${h * 2.6}px;background:#6366f1;border-radius:8px"></div>`,
  )
  .join("")}
</div></body></html>`

const item = {
  id: "tdo_sales",
  revision: 6,
  title: "Xuất doanh số tháng 10 cho cô Laura",
  notes: "## Plan\n1. Xuất bảng doanh số tháng 10.\n2. Vẽ biểu đồ theo tuần.",
  done_when: "",
  draft: "",
  due: { kind: "none" },
  assignee: { kind: "bot", name: "bot_main" },
  state: "doing",
  source: { kind: "owner" },
  created_by: { kind: "owner" },
  close_reason: "",
  evidence: [],
  stale: false,
  priority: 2,
  priority_reason: "Cô Laura cần trước buổi họp",
  chat_id: chat,
  created_at: at(-3 * hour),
  updated_at: at(-10 * 60_000),
}

const events = [
  {
    id: 1,
    at: at(-3 * hour),
    revision: 1,
    actor: { kind: "owner" },
    kind: "created",
    before: {},
    after: { title: item.title },
  },
  {
    id: 2,
    at: at(-3 * hour + 60_000),
    revision: 2,
    actor: { kind: "owner" },
    kind: "commented",
    before: {},
    after: { text: "Xuất giúp anh bảng doanh số tháng 10 và vẽ biểu đồ theo tuần." },
  },
  {
    id: 3,
    at: at(-10 * 60_000),
    revision: 6,
    actor: { kind: "bot", id: "bot_main" },
    kind: "commented",
    before: {},
    after: {
      text: `Xong: bảng doanh số tháng 10 và biểu đồ theo tuần (${chart}). Tuần 4 cao nhất.`,
      files: [
        "exports/doanh-so-thang-10.xlsx",
        "exports/tong-hop.pdf",
        "exports/ghi-chu.docx",
        "exports/don-hang-cu.csv",
      ],
      chat,
      artifacts: [chart],
    },
  },
]

async function serve(page: Page) {
  await page.context().route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/artifact-files/tok-chart-1/index.html") {
      return route.fulfill({
        status: 200,
        contentType: "text/html",
        headers: {
          "Access-Control-Allow-Origin": "*",
          "Content-Security-Policy": "sandbox allow-scripts allow-forms allow-downloads",
        },
        body: page1,
      })
    }
    if (path === `/artifacts/${chart}/revisions/1/frame`) {
      return json({ url: "/api/v1/artifact-files/tok-chart-1/" })
    }
    if (path === `/artifacts/${chart}`) {
      return json({
        id: chart,
        chat_id: chat,
        bot: "bot_main",
        revision: 1,
        title: "Doanh số tháng 10 theo tuần",
        type: "html",
        entry: "index.html",
        size: page1.length,
        created_at: at(-12 * 60_000),
        updated_at: at(-12 * 60_000),
        revisions: [
          {
            revision: 1,
            title: "Doanh số tháng 10 theo tuần",
            type: "html",
            entry: "index.html",
            size: page1.length,
            files: [{ path: "index.html", size: page1.length, media_type: "text/html" }],
            created_at: at(-12 * 60_000),
          },
        ],
      })
    }
    if (path === `/chats/${chat}/workspaces/bot/raw`) {
      if (url.searchParams.get("path") === "exports/doanh-so-thang-10.xlsx") {
        return route.fulfill({
          status: 200,
          contentType: "application/octet-stream",
          body: "PK fake workbook",
        })
      }

      return json(
        { code: "not_found", message: "exports/don-hang-cu.csv is not there any more." },
        404,
      )
    }
    if (path === "/inbox") return json({ data: [item], total: 1 })
    if (path === "/inbox/needs-you") return json({ count: 0 })
    if (path === `/inbox/${item.id}`) return json({ ...item, events })
    if (path === `/chats/${chat}`)
      return json({ id: chat, title: "Inbox: doanh số", bot: "bot_main" })

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/bots":
        return json({
          data: [
            {
              id: "bot_main",
              name: "Hypo",
              main: true,
              instructions: "",
              memory: "",
              memory_limit: 2200,
              skills: [],
              skills_off: [],
              tools: [],
              tools_off: [],
              connections: { limited: false, names: [] },
              workers: { limited: false, names: [] },
              scopes: {},
              created_at: at(-hour),
              updated_at: at(-hour),
            },
          ],
        })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })
}

const detail = (page: Page) => page.getByRole("article", { name: "Item" })
const result = (page: Page) => detail(page).getByRole("article", { name: "Comment by Hypo" })

// Scenario: the owner opens the item he handed to Hypo. Its last comment carries the export, as a chip that
// downloads it, and the chart, as its card; the chart's id in the text reads as its title and opens its page.
test("a Bot's comment shows its files and its Artifact", async ({ page }) => {
  await serve(page)
  await page.goto(`/inbox?item=todo%3A${item.id}`)

  const comment = result(page)
  await expect(
    comment.getByRole("button", { name: "Open Doanh số tháng 10 theo tuần" }),
  ).toBeVisible()
  await expect(comment.getByRole("link", { name: "Doanh số tháng 10 theo tuần" })).toHaveAttribute(
    "href",
    `/artifacts/${chart}`,
  )
  await expect(comment).not.toContainText(chart)
  const icons = comment.getByRole("list", { name: "Files" }).locator("img")
  await expect(icons).toHaveCount(4)
  await expect(icons.nth(0)).toHaveAttribute("src", /\/table\.svg$/)
  await expect(icons.nth(1)).toHaveAttribute("src", /\/pdf\.svg$/)
  await expect(icons.nth(2)).toHaveAttribute("src", /\/word\.svg$/)
  await comment.scrollIntoViewIfNeeded()
  await expect(comment.locator("iframe")).toBeVisible()
  await shot(page, "inbox-1207-result")

  const download = page.waitForEvent("download")
  await comment.getByRole("link", { name: "Download doanh-so-thang-10.xlsx" }).click()
  expect((await download).suggestedFilename()).toBe("doanh-so-thang-10.xlsx")

  await comment.getByRole("link", { name: "Download don-hang-cu.csv" }).click()
  const problem = comment.getByText("exports/don-hang-cu.csv is not there any more.")
  await expect(problem).toBeVisible()
  // The server's message is read whole on the chip, not cut (review of PR #1210).
  expect(await problem.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(true)
  await shot(page, "inbox-1207-missing-file")
})

// Scenario: review of PR #1210. On a phone with a keyboard, the owner opens the chart full screen and presses
// Escape: the chart closes and the item stays open.
test("Escape closes the full-screen Artifact, not the item", async ({ page }) => {
  await serve(page)
  await page.goto(`/inbox?item=todo%3A${item.id}`)

  await result(page).getByRole("button", { name: "Open Doanh số tháng 10 theo tuần" }).click()
  const full = page.getByRole("dialog", { name: "Artifact, full screen" })
  await expect(full).toBeVisible()

  await page.keyboard.press("Escape")
  await expect(full).toBeHidden()
  await expect(detail(page).getByRole("heading", { name: item.title })).toBeVisible()
})
