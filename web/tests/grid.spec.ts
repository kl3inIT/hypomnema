import { expect, test, type Locator, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The grid of chats, built, against a fake client API. A grid is a desktop's: a phone shows one chat.

const chats = ["health page", "login button", "slow setup", "quick question", "fifth"].map(
  (title, index) => ({
    id: `cht_${index + 1}`,
    title,
    bot: "coordinator",
    working: index === 0,
    coordinator: true,
    created_at: "2026-11-09T10:00:00Z",
    updated_at: "2026-11-09T10:00:00Z",
  }),
)

async function fakeServer(page: Page) {
  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const chat = chats.find((c) => path.startsWith(`/chats/${c.id}`))
    if (chat && path.endsWith("/stream")) {
      const said = {
        seq: 1,
        chat_id: chat.id,
        role: "owner",
        text: `about the ${chat.title}`,
        created_at: chat.created_at,
      }

      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `id: 1\nevent: message\ndata: ${JSON.stringify(said)}\n\n`,
      })
    }
    if (chat) return json(chat)

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
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
}

const row = (page: Page, title: string) =>
  page.getByRole("navigation", { name: "Chats" }).getByRole("link", { name: title })
const pane = (page: Page, title: string) => page.getByRole("region", { name: title })
const panes = (page: Page) => page.getByRole("main", { name: "Grid of chats" }).getByRole("region")

// drag carries something to a place in a pane, given as shares of its width and height.
async function drag(page: Page, from: Locator, to: Locator, across: number, down: number) {
  const box = (await to.boundingBox())!
  await from.hover()
  await page.mouse.down()
  await page.mouse.move(box.x + box.width * across, box.y + box.height * down, { steps: 12 })
}

test.beforeEach(({ page }, info) => {
  test.skip(info.project.name === "phone", "a phone shows one chat: there is no grid")
  void page
})

// Scenario: the owner follows several chats at once. A chat dragged from the sidebar onto the open chat's right
// side makes a grid of the two; more are dropped on edges until there are four, and a fifth is not taken. The grid
// is still there after a reload.
test("chats are dragged from the sidebar into a grid of up to four", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/chats/cht_1")
  await expect(page.getByText("about the health page")).toBeVisible()

  await drag(page, row(page, "login button"), page.getByRole("main"), 0.97, 0.5)
  await expect(page.getByTestId("grid-ghost")).toBeVisible()
  await shot(page, "grid-dragging")
  await page.mouse.up()

  await expect(page).toHaveURL(/\/grid$/)
  await expect(panes(page)).toHaveCount(2)
  await expect(pane(page, "health page").getByText("about the health page")).toBeVisible()
  await expect(pane(page, "login button").getByText("about the login button")).toBeVisible()
  await expect(page.getByRole("link", { name: /Grid 2 chats/ })).toBeVisible()

  await drag(page, row(page, "slow setup"), pane(page, "login button"), 0.5, 0.97)
  await page.mouse.up()
  await drag(page, row(page, "quick question"), pane(page, "health page"), 0.5, 0.97)
  await page.mouse.up()
  await expect(panes(page)).toHaveCount(4)
  await shot(page, "grid-four")

  // A fifth has no room: nothing is offered and nothing changes.
  await drag(page, row(page, "fifth"), pane(page, "health page"), 0.03, 0.5)
  await expect(page.getByTestId("grid-ghost")).toHaveCount(0)
  await page.mouse.up()
  await expect(panes(page)).toHaveCount(4)

  await page.reload()
  await expect(panes(page)).toHaveCount(4)
})

// Scenario: the owner rearranges and tidies the grid, whose panes are groups of tabs as in Orca. A tab dragged
// onto the middle of another pane joins it; a pane fills the screen and comes back; taking panes off leaves one
// pane, which is its chat's page again, with its tabs.
test("tabs move between panes, a pane fills the screen, and the grid ends in one pane", async ({
  page,
}) => {
  await fakeServer(page)
  const panesKept = {
    p1: { tabs: [{ kind: "chat", chat: "cht_1" }], active: "chat:cht_1" },
    p2: {
      tabs: [
        { kind: "chat", chat: "cht_2" },
        { kind: "chat", chat: "cht_3" },
      ],
      active: "chat:cht_2",
    },
  }
  await page.addInitScript({
    content: `localStorage.setItem('hypomnema.grid', '{"axis":"columns","children":["p1","p2"]}')
      localStorage.setItem('hypomnema.panes', ${JSON.stringify(JSON.stringify(panesKept))})`,
  })
  await page.goto("/grid")
  await expect(panes(page)).toHaveCount(2)
  await expect(pane(page, "login button").getByRole("tab")).toHaveText([
    "login button",
    "slow setup",
  ])
  await expect(page.getByRole("link", { name: /Grid 3 chats/ })).toBeVisible()

  // "slow setup" is carried by its tab into the middle of the other pane, and is in front there.
  await drag(
    page,
    pane(page, "login button").getByRole("tab", { name: "slow setup" }),
    pane(page, "health page"),
    0.5,
    0.6,
  )
  await expect(page.getByTestId("grid-ghost")).toBeVisible()
  await shot(page, "grid-tab-joining")
  await page.mouse.up()
  await expect(pane(page, "slow setup").getByRole("tab")).toHaveText(["health page", "slow setup"])
  await expect(pane(page, "login button").getByRole("tab")).toHaveText(["login button"])
  await expect(pane(page, "slow setup").getByText("about the slow setup")).toBeVisible()

  await pane(page, "slow setup").getByRole("button", { name: "Full screen" }).click()
  await expect(
    pane(page, "slow setup").getByRole("button", { name: "Back to the grid" }),
  ).toBeVisible()
  await shot(page, "grid-full-screen")
  await pane(page, "slow setup").getByRole("button", { name: "Back to the grid" }).click()

  // One pane left is its chat's page, the chat in front, with the pane's tabs.
  await pane(page, "login button").getByRole("button", { name: "Remove from the grid" }).click()
  await expect(page).toHaveURL(/\/chats\/cht_3$/)
  await expect(page.getByRole("tab")).toHaveText(["health page", "slow setup"])
  await expect(page.getByRole("link", { name: /Grid/ })).toHaveCount(0)
  expect(await page.evaluate<string | null>("localStorage.getItem('hypomnema.grid')")).toBeNull()
})
