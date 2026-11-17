import { expect, test, type Page } from "@playwright/test"

// The pages a Bot shows, opened on the control plane itself (tests/server, issue #737): its embedded build, its own
// policy on the web client, and each page loaded from its frame's address, whose answer carries the page's policy.
// A srcdoc frame took the web client's policy on top of the page's, which only these headers show: Vite's preview
// sends none. The CDN is the test's own, answered in the browser: a request the policy refused never reaches it.

const cdnScript = "https://cdn.jsdelivr.net/npm/hypomnema-e2e-stub@1.0.0/stub.js"

async function signIn(page: Page) {
  await page.goto("/")
  await page.getByLabel("Username").fill("owner")
  await page.getByLabel("Password").fill("browser tests")
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible()
}

// open opens an artifact's own page by its title, and collects what the browser refused on it.
async function open(page: Page, title: string) {
  const refused: string[] = []
  page.on("console", (m) => {
    if (m.type() === "error" && /Content.Security.Policy/i.test(m.text())) refused.push(m.text())
  })

  const listed = await page.request.get("/api/v1/artifacts")
  expect(listed.ok()).toBe(true)
  const { data } = (await listed.json()) as { data: { id: string; title: string }[] }
  const id = data.find((a) => a.title === title)?.id
  expect(id, title).toBeDefined()

  await page.goto(`/artifacts/${id}`)

  return { frame: page.frameLocator(`iframe[title="${title}"]`), refused }
}

test.beforeEach(async ({ page }) => {
  await page.context().route(cdnScript, (route) =>
    route.fulfill({
      contentType: "text/javascript",
      headers: { "Access-Control-Allow-Origin": "*" },
      body: 'window.fromCDN = "CDN script ran"',
    }),
  )
  await signIn(page)
})

// Scenario: the web client keeps its own strict policy, and a page in its frame runs its inline script and a
// script from a CDN.
test("a page runs its inline script and a CDN's", async ({ page }) => {
  const app = await page.request.get("/")
  expect(app.headers()["content-security-policy"]).toContain("script-src 'self';")

  const { frame, refused } = await open(page, "One page")

  await expect(frame.locator("#inline")).toHaveText("inline script ran")
  await expect(frame.locator("#cdn")).toHaveText("CDN script ran")
  expect(refused).toEqual([])
})

// Scenario: a folder page, as a bundler builds one, loads its module script and its styles by relative paths, and
// its script reads its data beside it; its origin stays opaque.
test("a folder page loads its relative script, styles and data", async ({ page }) => {
  const { frame, refused } = await open(page, "Folder page")

  await expect(frame.locator("#module")).toHaveText("module script ran")
  await expect(frame.locator("#data")).toHaveText("data read from the folder")
  await expect(frame.locator("#styled")).toHaveCSS("color", "rgb(0, 128, 0)")
  expect(refused).toEqual([])

  const iframe = page.locator('iframe[title="Folder page"]')
  await expect(iframe).toHaveAttribute("sandbox", "allow-scripts allow-forms")
  expect(await iframe.evaluate((f: { contentDocument: unknown }) => f.contentDocument)).toBeNull()
})

// Scenario: the pages skill's dashboard (issue #423) draws itself under the server's policy and the browser
// refuses nothing.
test("the pages skill's dashboard draws itself", async ({ page }) => {
  const { frame, refused } = await open(page, "Sales dashboard")

  await expect(frame.locator("html")).toHaveAttribute("data-ready", "true")
  await expect(frame.locator("#chart-regions svg rect")).toHaveCount(4)
  expect(refused).toEqual([])
})
