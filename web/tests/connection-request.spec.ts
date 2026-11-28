import { expect, test, type Page } from "@playwright/test"

import { openMenu } from "./menu.js"
import { shot } from "./shot.js"
import { stubPush } from "./push-stub.js"

// The grant card (issue #471, part D), built, against a fake client API: a Bot whose Connections list is limited asked,
// from its main chat, for one it was not given. The card is in the chat above the composer, in the Approval card's
// shape, with exactly two answers, Always for this Bot and Decline (owner, 2026-11-21); the pending request makes the
// Bot's row and its department's count Needs you. The answer is a line in the chat, which wakes the Bot.

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

const bot = (id: string, position: number, more: object = {}) => ({
  id,
  main: id === "coordinator",
  name: id,
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
  ...more,
})

const chat = (id: string, name: string, title: string) => ({
  id,
  title,
  bot: name,
  working: false,
  coordinator: true,
  created_at: ago(3600),
  updated_at: ago(600),
})

const asked = {
  id: "cnr_1",
  bot: "hr",
  chat_id: "cht_hr",
  integration: "lark",
  label: "report",
  reason: "To read this week's attendance report in Lark for the summary you asked for.",
  state: "pending",
  created_at: ago(60),
}

// fakeServer answers as the control plane. A decision is kept: the request leaves the pending list and its line is
// in the chat's history, as orchestration.DecideConnectionRequest writes it.
async function fakeServer(page: Page) {
  const request = { ...asked }
  const decisions: string[] = []
  const messages: object[] = [
    {
      seq: 1,
      chat_id: "cht_hr",
      role: "owner",
      text: "Sum up this week's attendance",
      created_at: ago(120),
    },
    {
      seq: 2,
      chat_id: "cht_hr",
      role: "coordinator",
      text: "I need the Lark report account for that; I asked you for it.",
      created_at: ago(60),
    },
  ]
  const bots = [
    bot("coordinator", 0, { main_chat: "cht_main" }),
    bot("hr", 1, {
      main_chat: "cht_hr",
      department: "dep_1",
      connections: { limited: true, names: ["lark:community"] },
    }),
  ]
  const chats = [chat("cht_main", "coordinator", "Plans"), chat("cht_hr", "hr", "Attendance")]

  await page.route("**/api/v1/**", async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/connection-requests/cnr_1/decision" && req.method() === "POST") {
      const decision: string = req.postDataJSON().decision
      decisions.push(decision)
      request.state = decision === "grant" ? "granted" : "declined"
      messages.push({
        seq: 3,
        chat_id: "cht_hr",
        role: "system",
        kind: "wake",
        text:
          decision === "grant"
            ? "Connection granted: the owner gave you lark:report, always for this Bot. Go on with what you asked it for."
            : "Connection declined: the owner did not give you lark:report. Do not ask for it again now; go on without it, or tell the owner what it would take.",
        created_at: new Date().toISOString(),
      })

      return json({ ...request, decided_at: new Date().toISOString() })
    }
    if (path === "/connection-requests")
      return json({ data: request.state === "pending" ? [request] : [] })

    const open = chats.find((c) => path.startsWith(`/chats/${c.id}`))
    if (open && path.endsWith("/stream")) {
      const mine = open.id === "cht_hr" ? messages : []

      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: mine
          .map(
            (m) =>
              `id: ${(m as { seq: number }).seq}\nevent: message\ndata: ${JSON.stringify(m)}\n\n`,
          )
          .join(""),
      })
    }
    if (open && path.endsWith("/messages"))
      return json({ data: [], next_after: 0, has_more: false })
    if (open && path === `/chats/${open.id}`) return json(open)

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/chats":
        return json({ data: chats })
      case "/bots":
        return json({ data: bots })
      case "/departments":
        return json({ data: [{ id: "dep_1", name: "People", position: 0 }] })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return decisions
}

const card = (page: Page) => page.getByRole("region", { name: "Connection asked for: lark:report" })
const sidebar = (page: Page) => page.getByRole("navigation", { name: "Bots" })

// Scenario: the owner sees the card in the Bot's main chat, with the Bot, the Connection and the reason, and exactly
// two answers; the Bot's row says Needs you and its folded department counts it. He answers Always for this Bot: the
// card goes and the chat says the Connection was granted.
test("the grant card answers Always for this Bot", async ({ page }) => {
  const decisions = await fakeServer(page)
  await page.goto("/chats/cht_hr")

  await expect(card(page)).toBeVisible()
  await expect(card(page)).toContainText("hr asks for lark:report")
  await expect(card(page)).toContainText("To read this week's attendance report")
  await expect(card(page).getByRole("button")).toHaveText(["Decline", "Always for this Bot"])
  await card(page).scrollIntoViewIfNeeded()
  await shot(page, "grant-card-pending")

  await openMenu(page)
  const row = sidebar(page).getByRole("link", { name: /^hr/ })
  await expect(row).toContainText("Needs you")
  await sidebar(page).getByRole("button", { name: "Collapse People" }).click()
  await expect(sidebar(page).getByTestId("department-needs-you")).toHaveText(/1/)
  await shot(page, "grant-card-needs-you")
  await sidebar(page).getByRole("button", { name: "Expand People" }).click()
  if ((page.viewportSize()?.width ?? 1280) < 768) await page.keyboard.press("Escape")

  await card(page).getByRole("button", { name: "Always for this Bot" }).click()
  await expect(card(page)).toHaveCount(0)
  expect(decisions).toEqual(["grant"])

  await page.reload()
  await expect(page.getByRole("note", { name: "Update from the control plane" })).toContainText(
    "Connection granted",
  )
  await shot(page, "grant-card-granted")
})

// Scenario: the owner declines: the card goes, the chat says so, and nothing waits on him any more.
test("the grant card answers Decline", async ({ page }) => {
  const decisions = await fakeServer(page)
  await page.goto("/chats/cht_hr")

  await card(page).getByRole("button", { name: "Decline" }).click()
  await expect(card(page)).toHaveCount(0)
  expect(decisions).toEqual(["decline"])

  await page.reload()
  await expect(page.getByRole("note", { name: "Update from the control plane" })).toContainText(
    "Connection declined",
  )
  await openMenu(page)
  await expect(sidebar(page).getByRole("link", { name: /^hr/ })).not.toContainText("Needs you")
  if ((page.viewportSize()?.width ?? 1280) < 768) await page.keyboard.press("Escape")
  await shot(page, "grant-card-declined")
})

// Scenario: something waits on the owner in this chat (issue #1047): the chat offers notifications once, above the
// card. Nothing is asked of the browser until he presses Turn on; "Not now" hides the offer, and it stays hidden
// after a reload while the card still waits.
// The Push API is stubbed (push-stub.ts): headless Chromium denies notifications, as a browser whose owner refused.
test("a chat where something waits offers notifications once", async ({ page }) => {
  await page.addInitScript(stubPush)
  await fakeServer(page)
  await page.goto("/chats/cht_hr")

  const offer = page.getByRole("complementary", { name: "Turn on notifications" })
  await expect(card(page)).toBeVisible()
  await expect(offer).toContainText("Get a notification when something waits on you")
  await expect(offer.getByRole("button", { name: "Turn on" })).toBeVisible()
  await shot(page, "notifications-offer")

  await offer.getByRole("button", { name: "Not now" }).click()
  await expect(offer).toHaveCount(0)

  await page.reload()
  await expect(card(page)).toBeVisible()
  await expect(offer).toHaveCount(0)
})
