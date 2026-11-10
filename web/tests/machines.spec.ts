import { expect, test, type Page } from "@playwright/test"

// The Machines page, built, against a fake client API, on a phone and a desktop.

const GiB = 2 ** 30
const now = new Date().toISOString()

const node = (id: string, name: string, m: object = {}) => ({
  id,
  name,
  status: "active",
  max_dispatches: 4,
  runtimes: [
    { runtime: "claude", path: "/usr/bin/claude", version: "2.1.0" },
    { runtime: "codex", path: "/usr/bin/codex", version: "0.50.0" },
  ],
  system: {
    daemon_version: "0.4.0",
    os: "linux",
    arch: "amd64",
    cpus: 8,
    memory_bytes: 32 * GiB,
    disk_bytes: 200 * GiB,
  },
  stats: {
    cpu_percent: 23.4,
    memory_used_bytes: 12 * GiB,
    disk_used_bytes: 150 * GiB,
    load1: 1.2,
    at: now,
  },
  last_seen_at: now,
  connected: true,
  active: 3,
  created_at: now,
  ...m,
})

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}

async function fakeServer(page: Page) {
  let nodes = [
    node("nod_1", "aioffice-app"),
    node("nod_2", "gpu-box", {
      status: "drained",
      active: 0,
      stats: {
        cpu_percent: 71,
        memory_used_bytes: 30 * GiB,
        disk_used_bytes: 40 * GiB,
        load1: 6.1,
        at: now,
      },
    }),
    node("nod_3", "old-laptop", {
      connected: false,
      active: 0,
      last_seen_at: new Date(Date.now() - 2 * 86_400_000).toISOString(),
    }),
  ]
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() !== "GET") calls.push(call)

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /nodes":
        return json({ data: nodes })
      case "POST /nodes": {
        const { name } = request.postDataJSON() as { name: string }
        const added = node("nod_4", name, {
          connected: false,
          active: 0,
          runtimes: [],
          system: undefined,
          stats: undefined,
          last_seen_at: undefined,
        })
        nodes = [...nodes, added]

        return json({ node: added, token: "hnt_shown_once" }, 201)
      }
      case "POST /nodes/nod_2/resume":
        nodes = nodes.map((n) => (n.id === "nod_2" ? { ...n, status: "active" } : n))

        return json(nodes[1])
      case "DELETE /nodes/nod_1":
        return json({ code: "conflict", message: "the node still runs 3 Dispatches" }, 409)
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
      case "GET /questions":
      case "GET /profiles":
        return json({ data: [] })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return calls
}

// Scenario: the owner looks at the machines as cards, then as a table that scrolls inside its box, and the
// choice survives a reload.
test("the machines show as cards and as a table", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/machines")

  await expect(page.getByText("1 online · 1 drained · 1 offline")).toBeVisible()
  const app = page.getByRole("article", { name: "aioffice-app" })
  await expect(app).toContainText("Online")
  await expect(app).toContainText("3 of 4")
  await expect(app.getByRole("meter", { name: "Disk" })).toHaveAttribute("data-tone", "warn")
  await expect(page.getByRole("article", { name: "old-laptop" })).toContainText(
    "Offline · seen 2d ago",
  )
  await shot(page, "machines-grid")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("row", { name: /gpu-box/ })).toContainText("Drained")
  await shot(page, "machines-table")
  // The page itself does not scroll sideways: the table does, inside its box.
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )

  await page.reload()
  await expect(page.getByRole("table")).toBeVisible()
})

// Scenario: the owner adds a machine and gets its token and install line once; then resumes a drained machine,
// and is refused when removing one that still works.
test("the owner adds, resumes and fails to remove a machine", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/machines")

  await page.getByRole("button", { name: "Add machine" }).click()
  const dialog = page.getByRole("dialog")
  await dialog.getByLabel("Name").fill("new-box")
  await dialog.getByRole("button", { name: "Add" }).click()
  await expect(dialog.getByLabel("Token", { exact: true })).toHaveText("hnt_shown_once")
  await expect(dialog.getByLabel("Install line", { exact: true })).toHaveText(
    "curl -fsSL http://127.0.0.1:4173/install/node.sh | sh",
  )
  await dialog.getByRole("button", { name: "Done" }).click()
  await expect(page.getByText("hnt_shown_once")).toHaveCount(0)
  await expect(page.getByRole("article", { name: "new-box" })).toContainText("Offline · never seen")

  await page.getByRole("button", { name: "Actions for gpu-box" }).click()
  await page.getByRole("menuitem", { name: "Resume" }).click()
  await expect(page.getByRole("article", { name: "gpu-box" })).toContainText("Online")

  await page.getByRole("button", { name: "Actions for aioffice-app" }).click()
  await page.getByRole("menuitem", { name: "Remove" }).click()
  const confirm = page.getByRole("alertdialog")
  await confirm.getByRole("button", { name: "Remove" }).click()
  await expect(confirm.getByRole("alert")).toHaveText("the node still runs 3 Dispatches")

  expect(calls).toEqual(["POST /nodes", "POST /nodes/nod_2/resume", "DELETE /nodes/nod_1"])
})
