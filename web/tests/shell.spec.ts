import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The built app against a fake client API (page.route), on a phone and a desktop.

const chat = {
  id: "cht_1",
  title: "health page",
  working: false,
  coordinator: true,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
}

const message = (seq: number, m: object) => ({
  seq,
  chat_id: chat.id,
  text: "",
  created_at: "2026-11-09T10:00:00Z",
  ...m,
})

// A stream as the server sends it: kept messages with ids, live frames without.
const frames = [
  'event: state\ndata: {"working":true}\n\n',
  `id: 1\nevent: message\ndata: ${JSON.stringify(message(1, { role: "owner", text: "add a health page" }))}\n\n`,
  `id: 2\nevent: message\ndata: ${JSON.stringify(
    message(2, {
      role: "coordinator",
      parts: [{ type: "thinking", text: "check the runs first" }],
    }),
  )}\n\n`,
  `id: 3\nevent: message\ndata: ${JSON.stringify(
    message(3, {
      role: "tool",
      text: "pi ran bash: hypomnema runs list --json",
      parts: [
        {
          type: "tool_result",
          id: "call_1",
          name: "bash",
          args: { command: "hypomnema runs list --json" },
          output: "[]",
        },
      ],
    }),
  )}\n\n`,
  `id: 4\nevent: message\ndata: ${JSON.stringify(
    message(4, {
      role: "coordinator",
      text: "Given to a worker: **run_1**.",
      parts: [{ type: "text", text: "Given to a worker: **run_1**." }],
    }),
  )}\n\n`,
  'event: state\ndata: {"working":false}\n\n',
].join("")

// signedIn says whether the browser already has a session; password is the one the server takes.
async function fakeServer(page: Page, signedIn: boolean, password = "correct horse") {
  const sent: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/me") {
      return signedIn
        ? json({ id: "usr_1", name: "admin", kind: "owner" })
        : json({ code: "unauthorized", message: "signing in is required" }, 401)
    }
    if (path === "/session" && route.request().method() === "POST") {
      const body = route.request().postDataJSON() as { username: string; password: string }
      if (body.username !== "admin" || body.password !== password) {
        return json({ code: "unauthorized", message: "the name or the password is wrong" }, 401)
      }

      signedIn = true

      return route.fulfill({ status: 204 })
    }
    if (path === "/session" && route.request().method() === "DELETE") {
      signedIn = false

      return route.fulfill({ status: 204 })
    }
    if (path === "/chats" && route.request().method() === "GET") return json({ data: [chat] })
    if (path === "/chats" && route.request().method() === "POST") return json(chat, 201)
    if (path === `/chats/${chat.id}`) return json(chat)
    if (path === `/chats/${chat.id}/stream`) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: frames })
    }
    if (path === `/chats/${chat.id}/messages`) {
      sent.push((route.request().postDataJSON() as { text: string }).text)
      return json(message(9, { role: "owner", text: sent.at(-1) }), 201)
    }

    return json({ error: "not found" }, 404)
  })

  return sent
}

// Scenario: the first visit asks for the name and the password and opens the chat screen, dark by default. Nothing
// is kept in the browser's storage. Signing out brings the sign-in screen back.
test("signing in with a name and a password opens the chats", async ({ page }) => {
  await fakeServer(page, false)
  await page.goto("/")

  await expect(page.locator("html")).toHaveClass(/dark/)
  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password").fill("correct horse")
  await page.getByRole("button", { name: "Sign in" }).click()

  await expect(page.getByRole("heading", { name: "What should be built?" })).toBeVisible()
  await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible()
  expect(await page.evaluate(`localStorage.getItem("hypomnema.token")`)).toBeNull()

  await page.getByRole("button", { name: "Sign out" }).click()
  await expect(page.getByLabel("Password")).toBeVisible()
})

// Scenario: the owner starts a chat; pi's work shows as one line that opens on its thought and the command with
// its output, and its reply is rendered as markdown.
test("a chat shows pi's work and reply", async ({ page }) => {
  const sent = await fakeServer(page, true)
  await page.goto("/")

  await page.getByRole("textbox", { name: "Message" }).fill("add a health page")
  await page.keyboard.press("Enter")

  await expect(page).toHaveURL(/\/chats\/cht_1$/)
  expect(sent).toEqual(["add a health page"])

  const log = page.getByRole("log")
  await expect(log.getByText("add a health page")).toBeVisible()
  await expect(log.locator('[data-streamdown="strong"]', { hasText: "run_1" })).toBeVisible()

  await log.getByText("Ran 1 tool").click()
  await expect(log.getByText("check the runs first")).toBeVisible()
  await log.getByText("hypomnema runs list --json").click()
  await expect(log.locator("pre", { hasText: "[]" })).toBeVisible()
})

// Scenario: a wrong password is refused, and the sign-in screen stays.
test("a wrong password is refused", async ({ page }) => {
  await fakeServer(page, false)
  await page.goto("/")

  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password").fill("not it")
  await page.getByRole("button", { name: "Sign in" }).click()

  await expect(page.getByRole("alert")).toContainText("The name or the password is wrong.")
  await shot(page, "sign-in-refused")
  await expect(page.getByLabel("Password")).toBeVisible()
})
