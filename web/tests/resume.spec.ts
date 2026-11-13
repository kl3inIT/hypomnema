import { expect, test, type Page } from "@playwright/test"

// Opening the app goes back to where the owner last was (owner, 2026-11-13), as OpenMausBot reopens its last
// conversation: built, against a fake client API.

const at = "2026-11-09T10:00:00Z"
const chat = {
  id: "cht_1",
  title: "health page",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: at,
  updated_at: at,
}

async function fakeServer(page: Page, chats: { current: (typeof chat)[] }) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/chats":
        return json({ data: chats.current })
      case "/chats/cht_1":
        return chats.current.length > 0
          ? json(chat)
          : json({ code: "not_found", message: "no chat cht_1" }, 404)
      case "/chats/cht_1/stream":
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      case "/bots":
        return json({ data: [{ name: "coordinator" }] })
      default:
        return json({ data: [] })
    }
  })
}

// Scenario: the owner chats, leaves, and opens the app again: the chat is there. New chat still opens the start
// page. A chat that was deleted meanwhile is not gone back to.
test("opening the app goes back to the chat the owner was in", async ({ page, isMobile }) => {
  const chats = { current: [chat] }
  await fakeServer(page, chats)

  await page.goto("/")
  await expect(page.getByRole("heading", { name: "What should be built?" })).toBeVisible()

  await page.goto("/chats/cht_1")
  await expect(page.getByRole("tab", { name: "health page" })).toBeVisible()

  // The app opened again, at its address.
  await page.goto("/")
  await expect(page).toHaveURL(/\/chats\/cht_1$/)
  await expect(page.getByRole("tab", { name: "health page" })).toBeVisible()

  // New chat, asked for, is the start page; on a phone the menu is the start page.
  await page
    .getByRole("link", { name: isMobile ? "Menu" : "New chat", exact: true })
    .first()
    .click()
  await expect(page.getByRole("heading", { name: "What should be built?" })).toBeVisible()
  await expect(page).toHaveURL(/\/$/)

  // The chat is deleted elsewhere: the app opens at the start page.
  chats.current = []
  await page.goto("/")
  await expect(page.getByRole("heading", { name: "What should be built?" })).toBeVisible()
  await expect(page).toHaveURL(/\/$/)
})
