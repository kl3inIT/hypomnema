import { expect, test, type Page, type WebSocketRoute } from "@playwright/test"

// The terminal page, built, against a fake client API and a fake terminal WebSocket.

const now = new Date().toISOString()
const node = {
  id: "nod_1",
  name: "build-1",
  status: "active",
  max_dispatches: 2,
  runtimes: [],
  connected: true,
  terminal: true,
  active: 0,
  created_at: now,
  last_seen_at: now,
}

// fakeServer answers the API and plays the node's shell on the WebSocket. It keeps the session ids tickets were
// asked for, the WebSockets it was given and what the page sent on them.
async function fakeServer(page: Page, refuse?: { status: number; message: string }) {
  const sessions: string[] = []
  const sockets: WebSocketRoute[] = []
  const sent: string[] = []

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const call = `${request.method()} ${new URL(request.url()).pathname.replace("/api/v1", "")}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /nodes":
        return json({ data: [node] })
      case "POST /nodes/nod_1/terminal": {
        if (refuse) return json({ code: "conflict", message: refuse.message }, refuse.status)

        sessions.push((request.postDataJSON() as { session_id: string }).session_id)

        return json({ ticket: `ticket-${sessions.length}`, expires_at: now }, 201)
      }
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /chats":
      case "GET /questions":
      case "GET /profiles":
        return json({ data: [] })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })
  await page.routeWebSocket(/\/api\/v1\/terminals\//, (ws) => {
    sockets.push(ws)
    ws.onMessage((message) => sent.push(String(message)))
    ws.send(Buffer.from(`welcome to shell ${sockets.length}\r\n$ `))
  })

  return { sessions, sockets, sent }
}

const screenText = (page: Page) => page.locator(".xterm-rows")

// Scenario: the owner opens a machine's terminal. The page puts a session id in its address, shows what the shell
// prints and sends what is typed; after a reload it asks for the same session, so the node returns the same shell.
test("a terminal shows the shell, sends what is typed and keeps its session across a reload", async ({
  page,
}) => {
  const server = await fakeServer(page)
  await page.goto("/machines/nod_1/terminal")

  await expect(page).toHaveURL(/reconnect=[0-9a-f-]{36}/)
  await expect(page.getByRole("heading", { name: "build-1" })).toBeVisible()
  await expect(screenText(page)).toContainText("welcome to shell 1")
  await expect(page.getByTestId("terminal-status")).toHaveText("Connected")

  await page.getByRole("application", { name: "Terminal on build-1" }).click()
  await page.keyboard.type("ls")
  await expect
    .poll(() => server.sent.filter((m) => m.includes('"data"')))
    .toEqual(['{"data":"l"}', '{"data":"s"}'])

  await page.reload()
  await expect(screenText(page)).toContainText("welcome to shell 2")
  expect(server.sessions).toHaveLength(2)
  expect(server.sessions[1]).toBe(server.sessions[0])
})

// Scenario: the shell exits, and later the connection drops. An ended shell is said so and offers a new session,
// which has a new id; a dropped connection is said so and is tried again by itself, with the same session.
test("an ended shell offers a new session and a dropped connection comes back by itself", async ({
  page,
}) => {
  const server = await fakeServer(page)
  await page.goto("/machines/nod_1/terminal")
  await expect(screenText(page)).toContainText("welcome to shell 1")

  await server.sockets[0].close({ code: 1000, reason: "the shell ended" })
  await expect(page.getByRole("alert")).toContainText("The shell ended.")
  expect(server.sessions).toHaveLength(1)

  await page.getByRole("button", { name: "New session" }).click()
  await expect(screenText(page)).toContainText("welcome to shell 2")
  expect(server.sessions[1]).not.toBe(server.sessions[0])

  await server.sockets[1].close({ code: 1006, reason: "" })
  await expect(page.getByRole("alert")).toContainText("The connection dropped")
  await expect(screenText(page)).toContainText("welcome to shell 3")
  await expect(page.getByRole("alert")).toHaveCount(0)
  expect(server.sessions[2]).toBe(server.sessions[1])
})

// Scenario: the machine's node opens no terminal. The page says why, in the server's words, and does not keep
// trying by itself.
test("a machine without a terminal says why", async ({ page }) => {
  await fakeServer(page, { status: 409, message: "node build-1 opens no terminal" })
  await page.goto("/machines/nod_1/terminal")

  await expect(page.getByRole("alert")).toContainText("node build-1 opens no terminal")
  await expect(page.getByRole("button", { name: "Try again" })).toBeVisible()
})
