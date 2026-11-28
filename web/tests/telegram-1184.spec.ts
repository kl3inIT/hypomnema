import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Issue #1184: the owner's Telegram. Its card on the Integrations page says the api_id is missing until he gives it;
// then he links it by QR code with his two-step password, or by phone number and the code Telegram sent, a wrong code
// asked again; and the Inbox's chats page has a Telegram tab with its counts, bots and channels skipped. On a phone
// and a computer, in light and dark. Every number, code, password and name is made up.

type Ask = { field: "phone" | "code" | "password"; hint?: string; again: boolean }

type Chat = {
  id: string
  name: string
  kind: "direct" | "group" | "bot"
  reads: boolean
  set_by: "default"
}

const telegramChats: Chat[] = [
  { id: "user:5001", name: "Trung", kind: "direct", reads: true, set_by: "default" },
  { id: "chat:6001", name: "Gia đình", kind: "group", reads: true, set_by: "default" },
  { id: "channel:7001", name: "VMAP AI Foundation", kind: "group", reads: true, set_by: "default" },
  { id: "user:5002", name: "BotFather", kind: "bot", reads: false, set_by: "default" },
  { id: "user:777000", name: "Telegram", kind: "bot", reads: false, set_by: "default" },
  { id: "channel:7002", name: "Tech News", kind: "bot", reads: false, set_by: "default" },
]

const countOf = (chats: Chat[]) => ({
  all: chats.length,
  read: chats.filter((c) => c.reads).length,
  skipped: chats.filter((c) => !c.reads).length,
  direct: chats.filter((c) => c.kind === "direct").length,
  group: chats.filter((c) => c.kind === "group").length,
  bot: chats.filter((c) => c.kind === "bot").length,
})

// serve answers as the control plane: Telegram with or without the owner's api_id, a linking whose next step after
// each answer is next's, and the chats the secretary reads.
async function serve(
  page: Page,
  client: boolean,
  next: (answer: string) => Ask = () => ask("code"),
) {
  let step: Record<string, unknown> = { id: "signin-1", url: "", expires_in_seconds: 900 }
  const telegram = {
    id: "telegram",
    name: "Telegram",
    kind: "device",
    services: [],
    connections: [],
    ...(!client && {
      unready:
        "the api_id is missing: make the application at my.telegram.org and keep its api_id and api_hash in the vault as the secrets telegram-api-id and telegram-api-hash",
    }),
  }
  const state = {
    // accept is the phone accepting the QR code of an account with a two-step password.
    accept: () => {
      step = { ...step, qr_code: undefined, ask: ask("password", "the cat") }
    },
  }

  await page.context().route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (call) {
      case "GET /me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "GET /integrations":
        return json({ data: [telegram] })
      case "POST /integrations/telegram/sign-ins": {
        const body = request.postDataJSON() as { flow?: string }
        step =
          body.flow === "phone"
            ? { ...step, ask: ask("phone") }
            : {
                ...step,
                qr_code: "tg://login?token=AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA",
                qr_expires_at: new Date(Date.now() + 30_000).toISOString(),
              }
        return json(step)
      }
      case "GET /integrations/telegram/sign-ins/signin-1":
        return json(step)
      case "POST /integrations/telegram/sign-ins/signin-1/answer": {
        const body = request.postDataJSON() as { value: string }
        step = { id: "signin-1", url: "", expires_in_seconds: 900, ask: next(body.value) }
        return json(step)
      }
      case "POST /integrations/telegram/sign-ins/signin-1":
        // Completing waits until the account is linked, longer than these tests.
        return new Promise(() => {})
      case "GET /inbox/chat-sources":
        return json({
          data: [
            {
              integration: "telegram",
              label: "main",
              account: "+84 90 000 0001",
              counts: countOf(telegramChats),
            },
          ],
          defaults: { direct: true, group: true, bot: false, bot_patterns: ["\\bOA\\b"] },
        })
      case "GET /inbox/chat-sources/telegram/main":
        return json({ data: telegramChats, counts: countOf(telegramChats), matched: 6 })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /inbox":
        return json({ data: [], total: 0 })
      case "GET /inbox/needs-you":
        return json({ count: 0 })
      case "GET /git/ssh-key":
        return json({ public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq0Zx4bU3m7 hypomnema" })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return state
}

const ask = (field: Ask["field"], hint?: string, again = false): Ask => ({ field, hint, again })

async function openTelegram(page: Page, isMobile: boolean) {
  await page.goto("/settings/integrations")
  if (isMobile) await page.keyboard.press("Escape")
  const card = page.getByTestId("integration-card").filter({ hasText: "Telegram" })
  await expect(card).toBeVisible()

  return card
}

for (const theme of ["light", "dark"] as const) {
  test.describe(`${theme} mode`, () => {
    test.beforeEach(async ({ page }) => {
      await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
    })

    // Until the owner gave his api_id and api_hash, the card says the api_id is missing and how to give it.
    test(`Telegram says its api_id is missing (${theme})`, async ({ page, isMobile }) => {
      await serve(page, false)
      const card = await openTelegram(page, isMobile)
      await expect(card).toContainText("Not set up")
      await expect(card).toContainText("Telegram: the api_id is missing")
      await expect(card.getByRole("link", { name: "Open the Vault" })).toBeVisible()
      await expect(card.getByRole("button", { name: "Link phone" })).toHaveCount(0)
      await shot(page, `telegram-not-set-up-${theme}`)
    })

    // Scenario: the QR code first, then the account's two-step password.
    test(`the owner links Telegram by QR code and password (${theme})`, async ({
      page,
      isMobile,
    }) => {
      const server = await serve(page, true)
      const card = await openTelegram(page, isMobile)
      await card.getByRole("button", { name: "Link phone" }).click()
      const dialog = page.getByRole("dialog")
      await expect(dialog.getByRole("button", { name: "Use phone number" })).toBeVisible()
      await shot(page, `telegram-link-start-${theme}`)

      await dialog.getByRole("button", { name: "Show the code" }).click()
      await expect(dialog.getByAltText("QR code to link Telegram")).toBeVisible()
      await expect(dialog).toContainText("Link Desktop Device")
      await shot(page, `telegram-link-qr-${theme}`)

      server.accept()
      await expect(dialog.getByLabel("Two-step password")).toBeVisible({ timeout: 5000 })
      await expect(dialog).toContainText("Your hint: the cat")
      await dialog.getByLabel("Two-step password").fill("hunter2")
      await shot(page, `telegram-link-password-${theme}`)
    })

    // Scenario: the phone number, then the code Telegram sent; a wrong code is asked again.
    test(`the owner links Telegram by phone number and code (${theme})`, async ({
      page,
      isMobile,
    }) => {
      await serve(page, true, (answer) =>
        answer.startsWith("+")
          ? ask("code", "the Telegram app")
          : ask("code", "the Telegram app", answer === "11111"),
      )
      const card = await openTelegram(page, isMobile)
      await card.getByRole("button", { name: "Link phone" }).click()
      const dialog = page.getByRole("dialog")
      await dialog.getByRole("button", { name: "Use phone number" }).click()

      await dialog.getByLabel("Phone number").fill("+84912345678")
      await shot(page, `telegram-link-phone-${theme}`)
      await dialog.getByRole("button", { name: "Continue" }).click()

      await dialog.getByLabel("Code").fill("11111")
      await dialog.getByRole("button", { name: "Continue" }).click()
      await expect(dialog).toContainText("That code was wrong; type it again.")
      await shot(page, `telegram-link-code-again-${theme}`)
    })

    // The Inbox's chats page: a Telegram tab with its counts; a person and the groups are read, bots and channels
    // skipped.
    test(`the Inbox's chats page has a Telegram tab (${theme})`, async ({ page, isMobile }) => {
      await serve(page, true)
      await page.goto("/inbox/chats")
      if (isMobile) await page.keyboard.press("Escape")

      await expect(page.getByRole("tab", { name: /Telegram\s*6/ })).toBeVisible()
      const row = (name: string) => page.getByTestId("secretary-chat").filter({ hasText: name })
      await expect(row("Trung")).toContainText("Direct · Read · Default")
      await expect(row("VMAP AI Foundation")).toContainText("Group · Read · Default")
      await expect(row("BotFather")).toContainText("Bot · Skipped · Default for bots")
      await expect(row("Tech News")).toContainText("Bot · Skipped · Default for bots")
      await shot(page, `telegram-chats-${theme}`)
    })
  })
}
