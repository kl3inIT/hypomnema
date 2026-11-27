import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// A Bot's reply that asks for files with MEDIA: lines (issue #699), built, against a fake client API: each line is a
// file to download under the reply's text, not text.

const at = "2026-11-17T10:00:00Z"
const chat = {
  id: "cht_1",
  title: "VMAP contributions",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: at,
  updated_at: at,
}

const reply = {
  seq: 2,
  chat_id: "cht_1",
  role: "coordinator",
  text: [
    "The sheet of contributions up to 15/11 is ready, every column kept.",
    "",
    "MEDIA:out/tuan/dong-gop-vmap-den-15-11.xlsx",
    "MEDIA:out/tuan/old-report.zip",
    "MEDIA:out/tuan/summary.pdf",
    "",
    "To send another file, write the line so:",
    "",
    "```",
    "MEDIA:out/report.xlsx",
    "```",
  ].join("\n"),
  created_at: at,
}

const messages = [
  {
    seq: 1,
    chat_id: "cht_1",
    role: "owner",
    text: "Make me the sheet of VMAP contributions up to 15/11.",
    created_at: at,
  },
  reply,
]

async function fakeServer(page: Page) {
  const asked: string[] = []

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/chats/cht_1/workspaces/bot/raw") {
      const file = url.searchParams.get("path") ?? ""
      asked.push(file)
      if (file === "out/tuan/dong-gop-vmap-den-15-11.xlsx") {
        return route.fulfill({
          status: 200,
          contentType: "application/octet-stream",
          headers: { "Content-Disposition": 'attachment; filename="dong-gop-vmap-den-15-11.xlsx"' },
          body: Buffer.from("PK\u0003\u0004 a workbook"),
        })
      }

      return json({ code: "not_found", message: `${file} is not there.` }, 404)
    }

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/chats":
        return json({ data: [chat] })
      case "/chats/cht_1":
        return json(chat)
      case "/chats/cht_1/messages":
        return json({ data: messages, next_after: 2, has_more: false })
      case "/chats/cht_1/stream":
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return asked
}

// Scenario: the Bot made a spreadsheet and named it with a MEDIA: line. The reply shows its text without the line,
// and a chip with the file's name and a sheet's icon that downloads it from the Bot's folder. A file that is not
// there any more says the server's message on its chip. A line in a code block stays text.
test("a reply's MEDIA: lines are files to download", async ({ page }) => {
  const asked = await fakeServer(page)
  await page.goto("/chats/cht_1")

  const log = page.getByRole("log")
  const files = log.getByRole("list", { name: "Files" })
  await expect(files.getByRole("link")).toHaveCount(3)
  await expect(files.locator("img")).toHaveCount(3)
  const icons = await files
    .locator("img")
    .evaluateAll((images) => images.map((image) => image.getAttribute("src")))
  expect(icons).toEqual([
    expect.stringMatching(/\/table\.svg$/),
    expect.stringMatching(/\/zip\.svg$/),
    expect.stringMatching(/\/pdf\.svg$/),
  ])
  await expect(log).toContainText("every column kept.")
  await expect(
    log.locator("pre, code").filter({ hasText: "MEDIA:out/report.xlsx" }),
  ).not.toHaveCount(0)
  await expect(log.getByText("MEDIA:out/tuan/dong-gop-vmap-den-15-11.xlsx")).toHaveCount(0)
  await shot(page, "chat-media")

  const download = page.waitForEvent("download")
  await files.getByRole("link", { name: "Download dong-gop-vmap-den-15-11.xlsx" }).click()
  expect((await download).suggestedFilename()).toBe("dong-gop-vmap-den-15-11.xlsx")

  await files.getByRole("link", { name: "Download old-report.zip" }).click()
  await expect(files).toContainText("out/tuan/old-report.zip is not there.")
  expect(asked).toEqual(["out/tuan/dong-gop-vmap-den-15-11.xlsx", "out/tuan/old-report.zip"])
  await shot(page, "chat-media-missing")
})
