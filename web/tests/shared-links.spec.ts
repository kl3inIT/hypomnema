import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"
import type { SharedLink } from "../src/api/generated/types.gen.js"

// The Shared links page of Settings (issue #765), built, against a fake client API, on a phone and a desktop.

const now = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()
const token = (c: string) => c.repeat(43)

const start: SharedLink[] = [
  {
    id: "shr_aaaaaaaaaaaaaaaaaaaaaaaaaa",
    artifact_id: "art_sales",
    label: "Laura",
    token: token("a"),
    path: `/s/${token("a")}`,
    created_at: iso(now - 86_400_000),
    views: 64,
    last_viewed_at: iso(now - 13 * 60_000),
    title: "Sales dashboard",
    type: "html",
    bot: "bot_sales",
    bot_name: "Sales",
    chat_id: "cht_sales",
  },
  {
    id: "shr_bbbbbbbbbbbbbbbbbbbbbbbbbb",
    artifact_id: "art_report",
    label: "Huy",
    token: token("b"),
    path: `/s/${token("b")}`,
    created_at: iso(now - 3 * 86_400_000),
    views: 0,
    title: "Weekly report",
    type: "pdf",
    bot: "bot_reports",
    bot_name: "Reports",
    chat_id: "cht_reports",
  },
]

async function fakeServer(page: Page, links: SharedLink[] = start) {
  let current = links
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() === "DELETE" && path.startsWith("/artifact-shares/")) {
      calls.push(call)
      current = current.filter((link) => `/artifact-shares/${link.id}` !== path)

      return route.fulfill({ status: 204 })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /artifact-shares":
        return json({ data: current })
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

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens Settings › Shared links from the Connections group. Every link is listed newest first
// with its artifact, whom it is for, its Bot, its views and when it was last viewed; the search finds a link by its
// artifact or by whom it is for.
test("the owner sees every shared link and finds one", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/shared-links")

  const laura = page.getByRole("row", { name: "Sales dashboard, for Laura" })
  await expect(laura).toContainText("Sales")
  await expect(laura).toContainText("64")
  await expect(laura).toContainText("13m ago")
  const huy = page.getByRole("row", { name: "Weekly report, for Huy" })
  await expect(huy).toContainText("Never")
  // The Bot reads as its name, not its id.
  await expect(huy).toContainText("Reports")
  await expect(huy).not.toContainText("bot_reports")
  await expect(page.getByText("2 links, 64 views in all.")).toBeVisible()
  await expect(page.getByRole("row").nth(1)).toHaveAccessibleName("Sales dashboard, for Laura")
  await expect(laura.getByRole("link", { name: "Sales dashboard" })).toHaveAttribute(
    "href",
    "/artifacts/art_sales",
  )
  expect(await fits(page)).toBe(true)
  await shot(page, "shared-links")

  const search = page.getByLabel("Search links by artifact or name")
  await search.fill("huy")
  await expect(huy).toBeVisible()
  await expect(laura).toHaveCount(0)
  await search.fill("sales")
  await expect(laura).toBeVisible()
  await expect(huy).toHaveCount(0)
  await search.fill("nobody")
  await expect(page.getByText("No link matches “nobody”.")).toBeVisible()
})

// Scenario: the owner copies a link, opens a row's menu, and revokes the link for Huy after a confirmation; the
// list then has Laura's only.
test("the owner copies a link and revokes another", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"])
  const calls = await fakeServer(page)
  await page.goto("/settings/shared-links")

  await page.getByRole("button", { name: "Copy the link for Laura" }).click()
  await expect(page.getByText("The link for Laura was copied")).toBeVisible()
  expect(await page.evaluate("navigator.clipboard.readText()")).toBe(
    `${new URL(page.url()).origin}/s/${token("a")}`,
  )

  await page.getByRole("button", { name: "More for the link for Huy" }).click()
  const menu = page.getByRole("menu")
  await expect(menu.getByRole("menuitem", { name: "Open the link" })).toHaveAttribute(
    "href",
    new RegExp(`/s/${token("b")}$`),
  )
  await expect(menu.getByRole("menuitem", { name: "Open its chat" })).toHaveAttribute(
    "href",
    "/chats/cht_reports",
  )
  await shot(page, "shared-links-menu")
  await menu.getByRole("menuitem", { name: "Revoke link…" }).click()

  const confirm = page.getByRole("alertdialog", { name: "Revoke the link for Huy?" })
  await expect(confirm).toContainText("stops working at once")
  await shot(page, "shared-links-revoke")
  await confirm.getByRole("button", { name: "Revoke link" }).click()

  await expect(page.getByRole("row", { name: "Weekly report, for Huy" })).toHaveCount(0)
  await expect(page.getByRole("row", { name: "Sales dashboard, for Laura" })).toBeVisible()
  expect(calls).toEqual(["DELETE /artifact-shares/shr_bbbbbbbbbbbbbbbbbbbbbbbbbb"])
})

// Scenario: with no link yet, the page says where links are made.
test("the page says where links are made when there is none", async ({ page }) => {
  await fakeServer(page, [])
  await page.goto("/settings/shared-links")

  await expect(page.getByText("No shared links yet")).toBeVisible()
  await expect(page.getByText("choose Share link…")).toBeVisible()
  await shot(page, "shared-links-empty")
})

// Scenario: Settings lists the page in its Connections group.
test("settings lists Shared links under Connections", async ({ page, isMobile }) => {
  await fakeServer(page)
  await page.goto(isMobile ? "/settings" : "/settings/vault")

  const menu = page.getByRole("navigation", { name: "Settings" })
  const connections = isMobile
    ? menu.getByRole("region", { name: "Connections" })
    : menu.locator("div").filter({ has: page.getByText("Connections", { exact: true }) })
  await expect(connections.getByRole("link")).toHaveText([
    "Integrations",
    "Channels",
    "Shared links",
    "Vault",
  ])
  await connections.getByRole("link", { name: "Shared links" }).click()
  await expect(page).toHaveURL(/\/settings\/shared-links$/)
  await expect(page.getByRole("heading", { name: "Shared links" })).toBeVisible()
})
