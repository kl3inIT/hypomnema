import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// On a phone the app's sidebar is a drawer behind the header's ≡ on every page, and Settings is a list of its
// sections, each a page headed by "‹ Settings" (issue #604): built, against a fake client API.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const bot = (name: string, position: number, mainChat?: string) => ({
  id: name,
  ...(mainChat && { main_chat: mainChat }),
  main: name === "coordinator",
  name,
  position,
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
  created_at: ago(86400),
  updated_at: ago(86400),
})

const chat = (id: string, title: string, owner: string, seconds: number) => ({
  id,
  title,
  bot: owner,
  working: false,
  coordinator: owner === "coordinator",
  created_at: ago(seconds),
  updated_at: ago(seconds),
})

const chats = [
  chat("cht_1", "Add a /health page", "coordinator", 60),
  chat("cht_2", "Fix the login redirect", "coordinator", 3600),
  chat("cht_3", "Weekly usage summary", "researcher", 7200),
]

async function fakeServer(page: Page) {
  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    const said = chats.find((c) => path === `/chats/${c.id}/stream`)
    const opened = chats.find((c) => path === `/chats/${c.id}`)

    if (said) {
      const message = {
        seq: 1,
        chat_id: said.id,
        role: "owner",
        text: `about ${said.title}`,
        created_at: said.created_at,
      }

      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `id: 1\nevent: message\ndata: ${JSON.stringify(message)}\n\n`,
      })
    }
    if (opened) return json(opened)
    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/bots":
        return json({ data: [bot("coordinator", 0, "cht_1"), bot("researcher", 1, "cht_3")] })
      case "/chats":
        return json({ data: chats })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      case "/browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      default:
        return json({ data: [] })
    }
  })
}

const drawer = (page: Page) => page.getByRole("dialog", { name: "Sidebar" })
const menu = (page: Page) => page.getByRole("button", { name: "Menu" })

// Scenario: in a chat the owner taps ≡; the app's sidebar slides in from the left with the places, the Bots and
// Settings. He opens coordinator's chats and picks another one: the drawer closes and the chat opens. Escape closes
// it too, and the focus is back on ≡.
test("≡ opens the sidebar as a drawer, and picking a chat closes it", async ({
  page,
  isMobile,
}) => {
  test.skip(!isMobile, "a desktop shows the sidebar beside the page")
  await fakeServer(page)
  await page.goto("/chats/cht_1")
  await expect(page.getByText("about Add a /health page")).toBeVisible()
  // The start screen keeps no copy of the menus under its composer.
  await expect(page.getByRole("navigation", { name: "Places" })).toHaveCount(0)

  await menu(page).click()
  await expect(drawer(page)).toBeVisible()
  await expect(drawer(page).getByRole("link", { name: "Team" })).toBeVisible()
  await expect(drawer(page).getByRole("link", { name: "Settings" })).toBeVisible()
  await expect(drawer(page).getByRole("button", { name: "Sign out" })).toBeVisible()
  await shot(page, "drawer-open")

  await page.keyboard.press("Escape")
  await expect(drawer(page)).toHaveCount(0)
  await expect(menu(page)).toBeFocused()

  // A Bot's row opens its main chat (issue #471) and closes the drawer.
  await menu(page).click()
  await drawer(page)
    .getByRole("link", { name: /^researcher/ })
    .click()
  await expect(drawer(page)).toHaveCount(0)
  await expect(page).toHaveURL(/\/chats\/cht_3$/)
  await expect(page.getByText("about Weekly usage summary")).toBeVisible()
})

// Scenario: in a Settings section the header's ≡ still opens the app's sidebar, and New chat in it goes to the start
// screen.
test("≡ opens the app's sidebar from Settings", async ({ page, isMobile }) => {
  test.skip(!isMobile, "a desktop shows the sidebar beside the page")
  await fakeServer(page)
  await page.goto("/settings/vault")
  await expect(page.getByRole("heading", { name: "Vault", level: 1 })).toBeVisible()

  await menu(page).click()
  await expect(drawer(page).getByRole("navigation", { name: "Bots" })).toBeVisible()
  await drawer(page).getByRole("link", { name: "New chat" }).click()
  await expect(drawer(page)).toHaveCount(0)
  await expect(page.getByRole("heading", { name: "What should be built?" })).toBeVisible()
})

// Scenario: on a phone Settings lists its sections under the desktop sidebar's headings; the owner taps Vault, reads
// it, and "‹ Settings" brings him back to the list. The sections are no longer a row of tabs.
test("Settings on a phone is a list of sections", async ({ page, isMobile }) => {
  test.skip(!isMobile, "a desktop opens Settings on a section")
  await fakeServer(page)
  await page.goto("/settings")

  const list = page.getByRole("navigation", { name: "Settings" })
  await expect(list.getByRole("region")).toHaveCount(5)
  await expect(list.getByRole("heading", { level: 2 })).toHaveText([
    "Preferences",
    "Gateway",
    "Agents",
    "Connections",
    "Workers",
  ])
  await expect(list.getByRole("region", { name: "Preferences" }).getByRole("link")).toHaveText([
    "About you",
    "Notifications",
  ])
  await expect(list.getByRole("region", { name: "Workers" }).getByRole("link")).toHaveText([
    "Agents",
    "Machines",
  ])
  await expect(list.getByRole("region", { name: "Connections" }).getByRole("link")).toHaveText([
    "Integrations",
    "Channels",
    "Shared links",
    "Vault",
  ])
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
  await shot(page, "settings-list")

  await list.getByRole("link", { name: "Vault" }).click()
  await expect(page).toHaveURL(/\/settings\/vault$/)
  await expect(page.getByRole("heading", { name: "Vault", level: 1 })).toBeVisible()
  await expect(page.getByRole("navigation", { name: "Settings sections" })).toHaveCount(0)
  await shot(page, "settings-section")

  await page.getByRole("link", { name: "Settings", exact: true }).click()
  await expect(page).toHaveURL(/\/settings$/)
  await expect(list.getByRole("link", { name: "Vault" })).toBeVisible()
})

// A desktop, whose sidebar lists the sections, opens Settings on its first section, as before, with no ≡ and no
// way back in the header.
test("Settings on a desktop opens on a section", async ({ page, isMobile }) => {
  test.skip(isMobile, "a phone lists the sections")
  await fakeServer(page)
  await page.goto("/settings")

  await expect(page).toHaveURL(/\/settings\/integrations$/)
  await expect(menu(page)).toBeHidden()
  await expect(page.getByRole("link", { name: "Settings", exact: true })).toBeHidden()
})
