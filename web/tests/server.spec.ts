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
// its script reads its data beside it; its origin stays opaque, and it hands its viewer a file of its folder
// (issue #843: a report a Routine built).
test("a folder page loads its relative script, styles and data", async ({ page }) => {
  const { frame, refused } = await open(page, "Folder page")

  await expect(frame.locator("#module")).toHaveText("module script ran")
  await expect(frame.locator("#data")).toHaveText("data read from the folder")
  await expect(frame.locator("#styled")).toHaveCSS("color", "rgb(0, 128, 0)")
  expect(refused).toEqual([])

  const iframe = page.locator('iframe[title="Folder page"]')
  await expect(iframe).toHaveAttribute("sandbox", "allow-scripts allow-forms allow-downloads")
  expect(await iframe.evaluate((f: { contentDocument: unknown }) => f.contentDocument)).toBeNull()

  const download = page.waitForEvent("download")
  await frame.locator("#report").click()
  expect((await download).suggestedFilename()).toBe("report.json")
})

// Scenario: the pages skill's dashboard (issue #423) draws itself under the server's policy and the browser
// refuses nothing.
test("the pages skill's dashboard draws itself", async ({ page }) => {
  const { frame, refused } = await open(page, "Sales dashboard")

  await expect(frame.locator("html")).toHaveAttribute("data-ready", "true")
  await expect(frame.locator("#chart-regions svg rect")).toHaveCount(4)
  expect(refused).toEqual([])
})

// Scenario: the owner shares the folder page with Laura (issue #745). In a browser with no session, the link's page
// shows it in the locked frame under the server's headers, its relative script and data loaded, never indexed or
// kept; after the owner revokes the link, the open page says so by itself at its next check, with no reload.
test("a view-only link shows the page without signing in until it is revoked", async ({
  page,
  browser,
}) => {
  const listed = await page.request.get("/api/v1/artifacts")
  const { data } = (await listed.json()) as { data: { id: string; title: string }[] }
  const id = data.find((a) => a.title === "Folder page")?.id
  expect(id).toBeDefined()

  const made = await page.request.post(`/api/v1/artifacts/${id}/shares`, {
    data: { label: "Laura" },
  })
  expect(made.status()).toBe(201)
  const share = (await made.json()) as { id: string; path: string }

  const laura = await browser.newContext()
  const viewer = await laura.newPage()
  expect((await laura.cookies()).length).toBe(0)
  await viewer.clock.install()

  const refused: string[] = []
  viewer.on("console", (m) => {
    if (m.type() === "error" && /Content.Security.Policy/i.test(m.text())) refused.push(m.text())
  })

  const opened = await viewer.goto(share.path)
  expect(opened?.headers()["x-robots-tag"]).toBe("noindex, nofollow")
  expect(opened?.headers()["cache-control"]).toBe("private, no-store")

  await expect(viewer.getByRole("heading", { name: "Folder page" })).toBeVisible()
  const frame = viewer.frameLocator('iframe[title="Folder page"]')
  await expect(frame.locator("#module")).toHaveText("module script ran")
  await expect(frame.locator("#data")).toHaveText("data read from the folder")
  expect(refused).toEqual([])

  const iframe = viewer.locator('iframe[title="Folder page"]')
  await expect(iframe).toHaveAttribute("sandbox", "allow-scripts allow-forms allow-downloads")
  expect(await iframe.evaluate((f: { contentDocument: unknown }) => f.contentDocument)).toBeNull()

  const revoked = await page.request.delete(`/api/v1/artifact-shares/${share.id}`)
  expect(revoked.status()).toBe(204)

  await viewer.clock.fastForward(31_000)
  await expect(viewer.getByText("This link was revoked, or never was.")).toBeVisible()
  await expect(iframe).toHaveCount(0)

  await laura.close()
})
