import { expect, test, type Page } from "@playwright/test"

import { openMenu } from "./menu.js"
import { shot } from "./shot.js"

// Deleting a chat (issue #484), built, against a fake client API: a chat's menu has Delete, which asks first, as
// t3code's thread menu; the server refuses a chat pi works in, and deleting the open chat goes to its Bot's new chat.
// The sidebar's Bot row carries its main chat's menu, and the Bot's page lists its other chats, each with its menu
// (issue #471).

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

const coordinator: Record<string, unknown> = {
  id: "coordinator",
  main: true,
  name: "coordinator",
  position: 0,
  persona: "",
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
  created_at: ago(86400),
  updated_at: ago(86400),
}

async function fakeServer(
  page: Page,
  chats = [
    chat("cht_1", "Add a /health page", 30),
    chat("cht_2", "Fix the header layout", 7200, { working: true }),
    chat("cht_3", "Old release notes", 3 * 86400),
  ],
) {
  const deleted: string[] = []
  const bot = { ...coordinator, main_chat: "cht_1" }

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
      // The server's ClearMainChat: a deleted main chat is no Bot's any more.
      if (bot.main_chat === open.id) delete (bot as { main_chat?: string }).main_chat

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
      case "/bots":
        return json({ data: [bot] })
      case "/bots/coordinator":
        return json(bot)
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
const others = (page: Page) => page.getByRole("list", { name: "Other chats of coordinator" })

// Scenario: the Bot's page lists its chats other than its main chat (issue #471), with a link to the main chat. The
// owner opens the menu of one and deletes it after the dialog says it cannot be undone and that its Runs stay; the
// row goes. Cancel keeps it. A chat pi works in is refused in the dialog, in the server's words, and stays; it opens
// from the list.
test("a chat's menu deletes it after asking", async ({ page }) => {
  const deleted = await fakeServer(page)
  await page.goto("/bots/coordinator")

  await expect(others(page).getByRole("link")).toHaveText([
    /Fix the header layout.*Working/,
    /Old release notes/,
  ])
  await expect(page.getByRole("link", { name: "Open the main chat" })).toHaveAttribute(
    "href",
    "/chats/cht_1",
  )
  await others(page).scrollIntoViewIfNeeded()
  await shot(page, "bot-other-chats")
  const menu = others(page).getByRole("button", { name: "Actions for Old release notes" })
  await menu.click()
  await expect(page.getByRole("menuitem", { name: "Delete" })).toBeVisible()
  await shot(page, "chat-delete-menu")

  await page.getByRole("menuitem", { name: "Delete" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Delete “Old release notes”?")
  await expect(dialog).toContainText("This cannot be undone. Runs started here stay.")
  await shot(page, "chat-delete-dialog")

  await dialog.getByRole("button", { name: "Cancel" }).click()
  await expect(dialog).toHaveCount(0)
  expect(deleted).toEqual([])

  await menu.click()
  await page.getByRole("menuitem", { name: "Delete" }).click()
  await dialog.getByRole("button", { name: "Delete" }).click()
  await expect(page.getByText("Chat deleted")).toBeVisible()
  await expect(others(page).getByRole("link")).toHaveText([/Fix the header layout/])
  expect(deleted).toEqual(["cht_3"])

  await others(page).getByRole("button", { name: "Actions for Fix the header layout" }).click()
  await page.getByRole("menuitem", { name: "Delete" }).click()
  await dialog.getByRole("button", { name: "Delete" }).click()
  await expect(dialog).toContainText("stop it first")
  await shot(page, "chat-delete-refused")
  await dialog.getByRole("button", { name: "Cancel" }).click()
  expect(deleted).toEqual(["cht_3"])

  // A chat off the sidebar still opens from here, by its own address.
  await others(page)
    .getByRole("link", { name: /Fix the header layout/ })
    .click()
  await expect(page).toHaveURL(/\/chats\/cht_2$/)
})

// Scenario: the owner deletes the Bot's main chat, open, from its row's menu in the sidebar (issue #503); the page
// goes to a new chat with its Bot, and the row, with no main chat left, stands for its latest chat (issue #923).
test("deleting the open main chat goes to its Bot's new chat", async ({ page }) => {
  const deleted = await fakeServer(page)
  await page.goto("/chats/cht_1")
  await openMenu(page)

  await bots(page)
    .getByRole("link", { name: /coordinator/ })
    .hover()
  await bots(page).getByRole("button", { name: "Actions for coordinator" }).click()
  await expect(page.getByRole("menuitem", { name: "Delete" })).toBeVisible()
  await shot(page, "chat-delete-only-chat-menu")
  await page.getByRole("menuitem", { name: "Delete" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Delete “Add a /health page”?")
  await dialog.getByRole("button", { name: "Delete" }).click()

  await expect(page).toHaveURL(/\/\?bot=coordinator$/)
  expect(deleted).toEqual(["cht_1"])
  // A phone's drawer stays open over the new chat.
  if (!(await page.getByRole("dialog", { name: "Sidebar" }).isVisible())) await openMenu(page)
  await expect(bots(page).getByRole("link", { name: /coordinator/ })).toContainText(
    "Fix the header layout",
  )
})
