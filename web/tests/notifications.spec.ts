import { expect, test, type Page } from "@playwright/test"
import { shot } from "./shot.js"
import { stubPush } from "./push-stub.js"
import type { PushSubscription } from "../src/api/generated/types.gen.js"

// The Notifications page (issue #1047), built, against a fake client API and a stubbed Push API, on a phone and a
// desktop. A real push service and a real device are what the Go tests' fake push service and the owner's own
// iPhone prove.

const iPhone =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Mobile/15E148 Safari/604.1"

const now = new Date().toISOString()
const publicKey =
  "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM"

async function fakeServer(page: Page, start: PushSubscription[] = []) {
  let devices = start
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/me") return json({ id: "tok_1", name: "web", kind: "owner" })

    if (path.startsWith("/push/")) {
      calls.push(call)

      if (call === "GET /push/subscriptions") return json({ public_key: publicKey, data: devices })

      if (call === "PUT /push/subscriptions") {
        const body = request.postDataJSON() as {
          endpoint: string
          label: string
          keys: { p256dh: string; auth: string }
        }
        calls.push(`  ${body.endpoint} ${body.keys.p256dh} ${body.keys.auth} ${body.label}`)
        const kept: PushSubscription = {
          id: "psb_this",
          endpoint: body.endpoint,
          label: body.label,
          created_at: now,
          updated_at: now,
        }
        devices = [...devices.filter((d) => d.endpoint !== body.endpoint), kept]

        return json(kept)
      }

      const [, , , id, sub] = path.split("/")
      if (request.method() === "DELETE") devices = devices.filter((d) => d.id !== id)
      if (request.method() === "DELETE" || sub === "test") return route.fulfill({ status: 204 })
    }

    switch (call) {
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return calls
}

const laptop: PushSubscription = {
  id: "psb_laptop",
  endpoint: "https://fcm.googleapis.com/fcm/send/laptop",
  label: "Mac · Chrome",
  created_at: "2026-11-20T08:00:00Z",
  updated_at: "2026-11-20T08:00:00Z",
}

const card = (page: Page) => page.locator("[data-slot=card]").filter({ hasText: "This device" })
const device = (page: Page, name: string) => page.getByRole("listitem", { name, exact: true })

// Scenario: the owner opens Notifications from Settings on a browser that is not told yet: his laptop is listed. He
// turns notifications on: the browser asks its permission only then, subscribes with the control plane's key, and
// the subscription is kept with this device's name, which the list marks as this device. He sends it a test, then
// removes it, which drops the browser's subscription too.
test("the owner turns notifications on here, tests and removes the device", async ({ page }) => {
  await page.addInitScript(stubPush)
  const calls = await fakeServer(page, [laptop])
  await page.goto("/settings")
  await page.getByRole("link", { name: "Notifications" }).click()

  await expect(page.getByRole("heading", { name: "Notifications" })).toBeVisible()
  await expect(card(page)).toContainText("This device is not told yet.")
  await expect(device(page, "Mac · Chrome")).toBeVisible()
  expect(await page.evaluate("window.pushCalls")).toEqual([])
  await shot(page, "notifications-off")

  await page.getByRole("button", { name: "Turn on notifications" }).click()
  await expect(card(page)).toContainText("This device is told when something waits on you.")
  expect(await page.evaluate("window.pushCalls")).toEqual(["requestPermission", "subscribe"])

  // The page names this device from its user agent: Pixel 7's says Android, Desktop Chrome's Windows.
  const label = await page.evaluate(
    `navigator.userAgent.includes("Android") ? "Android · Chrome" : "Windows · Chrome"`,
  )
  const here = device(page, label as string)
  await expect(here).toContainText("This device")
  await expect(device(page, "Mac · Chrome")).not.toContainText("This device")
  await shot(page, "notifications-on")

  await card(page).getByRole("button", { name: "Send a test" }).click()
  await expect(page.getByText(`Test notification sent to ${label}`)).toBeVisible()

  await here.getByRole("button", { name: `Remove ${label}` }).click()
  await expect(here).toHaveCount(0)
  await expect(card(page)).toContainText("This device is not told yet.")
  expect(await page.evaluate("window.pushCalls")).toEqual([
    "requestPermission",
    "subscribe",
    "unsubscribe",
  ])

  expect(calls.filter((c) => !c.startsWith("GET"))).toEqual([
    "PUT /push/subscriptions",
    `  https://push.example/this-browser BUserKey authSecret ${label}`,
    "POST /push/subscriptions/psb_this/test",
    "DELETE /push/subscriptions/psb_this",
  ])
})

test.describe("on an iPhone in Safari", () => {
  test.use({ userAgent: iPhone })

  // Scenario: the owner opens Notifications in Safari on his iPhone, not from the Home Screen. Safari gives no push
  // there, so the page shows the steps to add Hypomnema to the Home Screen instead of a button, and asks nothing.
  test("the page shows the steps to add the app to the Home Screen", async ({ page }) => {
    await page.addInitScript(stubPush)
    await fakeServer(page, [laptop])
    await page.goto("/settings/notifications")

    const steps = page.getByRole("list", { name: "Add Hypomnema to the Home Screen" })
    await expect(steps).toContainText("Add to Home Screen")
    await expect(card(page)).toContainText("opened from the Home Screen")
    await expect(page.getByRole("button", { name: "Turn on notifications" })).toHaveCount(0)
    expect(await page.evaluate("window.pushCalls")).toEqual([])
    await shot(page, "notifications-ios-install")
  })
})
