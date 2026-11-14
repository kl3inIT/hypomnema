import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Deleting a chat from the sidebar (issue #484), built, against a fake client API: a chat row's menu has Delete,
// which asks first, as t3code's thread menu; the server refuses a chat pi works in, and deleting the open chat goes
// to its Bot's new chat.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const chat = (id: string, title: string, updated: number, m: object = {}) => ({
  id,
  title,
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: ago(updated + 600),
  updated_at: ago(updated),
  ...m,
})

async function fakeServer(page: Page) {
  const chats = [
    chat("cht_1", "Add a /health page", 30, { working: true }),
    chat("cht_2", "Fix the header layout", 7200),
    chat("cht_3", "Old release notes", 3 * 86400),
  ]
  const deleted: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const open = chats.find((c) => path.startsWith(`/chats/${c.id}`))
    if (open && request.method() === "DELETE" && path === `/chats/${open.id}`) {
      if (open.working) {
        return json(
          {
            code: "conflict",
            message: `the coordinator is working in chat ${open.id}; stop it first`,
          },
          409,
        )
      }
      chats.splice(chats.indexOf(open), 1)
      deleted.push(open.id)

      return json({ files_left: [] })
    }
    if (open && path.endsWith("/stream")) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
    }
    if (open && path === `/chats/${open.id}`) return json(open)

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/chats":
        return json({ data: chats })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return deleted
}

const bots = (page: Page) => page.getByRole("navigation", { name: "Bots" })
const chatsOf = (page: Page) => bots(page).getByRole("list", { name: "Chats of coordinator" })

async function openChats(page: Page) {
  await bots(page).getByRole("button", { name: "Show the chats of coordinator" }).click()
  await chatsOf(page).scrollIntoViewIfNeeded()
}

// Scenario: the owner opens a chat row's menu and deletes the chat after the dialog says it cannot be undone and that
// its Runs stay; the row goes. Cancel keeps it. A chat pi works in is refused in the dialog, in the server's words,
// and stays.
test("a chat's menu deletes it after asking", async ({ page }) => {
  const deleted = await fakeServer(page)
  await page.goto("/")
  await openChats(page)

  const menu = chatsOf(page).getByRole("button", { name: "Actions for Fix the header layout" })
  await chatsOf(page)
    .getByRole("link", { name: /Fix the header layout/ })
    .hover()
  await menu.click()
  await expect(page.getByRole("menuitem", { name: "Delete" })).toBeVisible()
  await shot(page, "chat-delete-menu")

  await page.getByRole("menuitem", { name: "Delete" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Delete “Fix the header layout”?")
  await expect(dialog).toContainText("This cannot be undone. Runs started here stay.")
  await shot(page, "chat-delete-dialog")

  await dialog.getByRole("button", { name: "Cancel" }).click()
  await expect(dialog).toHaveCount(0)
  expect(deleted).toEqual([])

  await menu.click()
  await page.getByRole("menuitem", { name: "Delete" }).click()
  await dialog.getByRole("button", { name: "Delete" }).click()
  await expect(page.getByText("Chat deleted")).toBeVisible()
  await expect(chatsOf(page).getByRole("link")).toHaveText([
    /Add a \/health page/,
    /Old release notes/,
  ])
  expect(deleted).toEqual(["cht_2"])

  await chatsOf(page)
    .getByRole("link", { name: /Add a \/health page/ })
    .hover()
  await chatsOf(page).getByRole("button", { name: "Actions for Add a /health page" }).click()
  await page.getByRole("menuitem", { name: "Delete" }).click()
  await dialog.getByRole("button", { name: "Delete" }).click()
  await expect(dialog).toContainText("stop it first")
  await shot(page, "chat-delete-refused")
  await dialog.getByRole("button", { name: "Cancel" }).click()
  expect(deleted).toEqual(["cht_2"])
})

// Scenario: the owner deletes the chat he has open; the page goes to a new chat with its Bot.
test("deleting the open chat goes to its Bot's new chat", async ({ page, isMobile }) => {
  test.skip(isMobile, "a phone lists the Bots on the start page only, where no chat is open")

  const deleted = await fakeServer(page)
  await page.goto("/chats/cht_3")
  await openChats(page)

  await chatsOf(page)
    .getByRole("link", { name: /Old release notes/ })
    .hover()
  await chatsOf(page).getByRole("button", { name: "Actions for Old release notes" }).click()
  await page.getByRole("menuitem", { name: "Delete" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete" }).click()

  await expect(page).toHaveURL(/\/\?bot=coordinator$/)
  expect(deleted).toEqual(["cht_3"])
})
