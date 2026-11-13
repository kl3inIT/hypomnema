import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Changing a chat's folders from the Files panel (issue #297), built, against a fake client API that keeps the
// folder in memory and refuses a save over a file that changed, as the machines do.

const at = "2026-11-09T10:00:00Z"
const chat = {
  id: "cht_1",
  title: "health page",
  bot: "coordinator",
  working: false,
  coordinator: true,
  created_at: at,
  updated_at: at,
}
const workspace = {
  id: "tsk_1",
  kind: "task",
  name: "add GET /health",
  root: "/srv/hyp/run_1/tsk_1",
  online: true,
  node_name: "stc-hy-app",
}

// A 1×1 PNG.
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
  "base64",
)

type Folder = {
  files: Map<string, { content: string; version: number }>
  dirs: Set<string>
  asked: string[]
  bodies: Record<string, unknown>[]
  uploads: Map<string, { path: string; bytes: number }>
}

function folder(): Folder {
  return {
    files: new Map([
      ["src/app.ts", { content: "export const app = 1\n", version: 1 }],
      [
        "docs/guide.md",
        { content: "# Guide\n\nThe logo:\n\n![logo](../img/logo.png)\n", version: 1 },
      ],
      ["img/logo.png", { content: "", version: 1 }],
      ["README.md", { content: "# demo\n", version: 1 }],
    ]),
    dirs: new Set(["src", "docs", "img"]),
    asked: [],
    bodies: [],
    uploads: new Map(),
  }
}

const sha = (path: string, version: number) => `${path}@${version}`

async function fakeServer(page: Page, f: Folder) {
  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })
    const refuse = (code: string, message: string, status: number) =>
      json({ code, message }, status)

    const ws =
      /^\/chats\/cht_1\/workspaces\/tsk_1\/(tree|file|raw|changes|folders|move|uploads(?:\/chunk|\/commit|\/abort)?)$/.exec(
        path,
      )
    if (ws) {
      const op = ws[1]
      const where = url.searchParams.get("path") ?? ""
      const body = request.postDataJSON() as Record<string, string> | null
      f.asked.push(`${request.method()} ${op} ${body?.path ?? where}`)
      if (body) f.bodies.push(body)

      if (op === "changes") return json({ git: true, changes: [], truncated: false })
      if (op === "uploads" && body) {
        const upload = `u${f.uploads.size + 1}`
        f.uploads.set(upload, { path: body.path, bytes: 0 })

        return json({ path: body.path, upload }, 201)
      }
      if (op === "uploads/chunk" && body) {
        const up = f.uploads.get(body.upload)
        const piece = Buffer.from(body.data ?? "", "base64").length
        if (!up || Number(body.offset) !== up.bytes) return refuse("conflict", "out of order", 409)
        up.bytes += piece

        return route.fulfill({ status: 204 })
      }
      if (op === "uploads/commit" && body) {
        const up = f.uploads.get(body.upload)
        const there = f.files.get(body.path)
        if (!up) return refuse("not_found", "no upload", 404)
        if (there && body.expect_sha256 !== sha(body.path, there.version)) {
          return refuse("conflict", `${body.path} is already there`, 409)
        }
        const version = (there?.version ?? 0) + 1
        f.files.set(body.path, { content: `${up.bytes} bytes`, version })

        return json({ path: body.path, sha256: sha(body.path, version) })
      }
      if (op === "uploads/abort") return route.fulfill({ status: 204 })
      if (op === "tree") {
        const prefix = where ? `${where}/` : ""
        const names = new Map<string, "file" | "directory">()
        for (const dir of f.dirs) {
          if (dir.startsWith(prefix) && !dir.slice(prefix.length).includes("/")) {
            names.set(dir, "directory")
          }
        }
        for (const file of f.files.keys()) {
          if (file.startsWith(prefix) && !file.slice(prefix.length).includes("/")) {
            names.set(file, "file")
          }
        }
        const entries = [...names].map(([p, type]) => ({
          name: p.slice(prefix.length),
          path: p,
          type,
          bytes: 1,
          modified_at: 1,
        }))

        return json({ entries, has_more: false })
      }
      if (op === "raw") {
        return route.fulfill({ status: 200, contentType: "application/octet-stream", body: png })
      }
      if (op === "folders" && body) {
        f.dirs.add(body.path)

        return json({ path: body.path }, 201)
      }
      if (op === "move" && body) {
        const file = f.files.get(body.path)
        if (file) {
          f.files.delete(body.path)
          f.files.set(body.to, file)
        }

        return json({ path: body.to })
      }
      // file
      if (request.method() === "GET") {
        const file = f.files.get(where)
        if (!file) return refuse("not_found", `nothing is at ${where}`, 404)

        return json({
          path: where,
          bytes: file.content.length,
          binary: false,
          truncated: false,
          content: file.content,
          sha256: sha(where, file.version),
        })
      }
      if (request.method() === "DELETE") {
        f.files.delete(where)
        f.dirs.delete(where)

        return route.fulfill({ status: 204 })
      }
      if (request.method() === "PUT" && body) {
        const file = f.files.get(body.path)
        if (body.expect_sha256 === undefined && file) {
          return refuse("conflict", `${body.path} is already there`, 409)
        }
        if (body.expect_sha256 !== undefined && !file) {
          return refuse("not_found", `${body.path} is not there any more`, 404)
        }
        if (file && body.expect_sha256 !== sha(body.path, file.version)) {
          return refuse("conflict", `${body.path} changed since it was opened`, 409)
        }
        const version = (file?.version ?? 0) + 1
        f.files.set(body.path, { content: body.content ?? "", version })

        return json({ path: body.path, sha256: sha(body.path, version) })
      }
    }

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/chats":
        return json({ data: [chat] })
      case "/chats/cht_1":
        return json(chat)
      case "/chats/cht_1/workspaces":
        return json({ data: [workspace] })
      case "/chats/cht_1/stream":
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "/browser":
        return json({ code: "unavailable", message: "this control plane has no browser" }, 503)
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })
}

const panel = (page: Page) => page.getByRole("complementary", { name: "Files" })

// tree opens the panel's All files when it is not shown: on a phone the panel gives way to a file it opened.
async function tree(page: Page) {
  // The Files button says whether the panel is open; a panel half hidden by a closing dialog is still open.
  const files = page.getByRole("button", { name: "Files", exact: true })
  if ((await files.getAttribute("aria-pressed")) !== "true") await files.click()
  await expect(panel(page)).toBeVisible()
  const all = panel(page).getByRole("button", { name: "All files" })
  if ((await all.getAttribute("aria-pressed")) !== "true") await all.click()

  return panel(page).getByRole("tree", { name: "All files" })
}

async function openFile(page: Page, folders: string[], name: string) {
  for (const dir of folders) {
    const item = (await tree(page)).getByRole("treeitem", { name: dir, exact: true })
    if ((await item.getAttribute("aria-expanded")) !== "true") await item.click()
  }
  await (await tree(page)).getByRole("treeitem", { name, exact: true }).click()
}

async function typeInEditor(page: Page, text: string) {
  // Tabs behind keep their editors; the one shown is typed into.
  await page.locator('[data-testid="code-view"]:visible .view-lines').click()
  await page.keyboard.press("ControlOrMeta+End")
  await page.keyboard.type(text)
}

// Scenario: the owner reads a guide as it reads, its image from the folder, then edits it and saves; the agent
// changes the file while the owner edits again, and the owner is told and overwrites. An image opens as one. A new
// file is made from a folder's menu and opened; a file is deleted after the owner says so; a tab with text not
// saved asks before it closes.
test("the owner edits, saves and changes a chat's folder from the Files panel", async ({
  page,
}) => {
  // One story of seven steps, each waiting on Monaco or a dialog: longer than the default 30 s on CI's machines.
  test.setTimeout(120_000)
  const f = folder()
  await fakeServer(page, f)
  await page.goto("/chats/cht_1")

  // A Markdown file opens as its preview, its relative image read from the folder.
  await openFile(page, ["docs"], "guide.md")
  await expect(page.getByRole("heading", { name: "Guide" })).toBeVisible()
  await expect(page.getByRole("img", { name: "logo" })).toHaveAttribute("src", /^blob:/)
  expect(f.asked).toContain("GET raw img/logo.png")
  await shot(page, "files-markdown")

  // Its source is edited and saved over the text it was opened as.
  await page.getByRole("button", { name: "Show the source" }).click()
  await typeInEditor(page, "More words.")
  await expect(page.getByLabel("not saved")).toBeVisible()
  await page.getByRole("button", { name: "Save", exact: true }).click()
  await expect(page.getByLabel("not saved")).toHaveCount(0)
  expect(f.bodies.at(-1)).toMatchObject({
    path: "docs/guide.md",
    expect_sha256: "docs/guide.md@1",
  })
  expect(f.files.get("docs/guide.md")?.content).toContain("More words.")

  // The agent writes the file while the owner edits it: the tab says so, and Overwrite saves over the new text.
  const now = f.files.get("docs/guide.md")
  f.files.set("docs/guide.md", {
    content: "# The agent's guide\n",
    version: (now?.version ?? 0) + 1,
  })
  await typeInEditor(page, " Mine.")
  await expect(page.getByText("This file changed on its machine since you opened it.")).toBeVisible(
    { timeout: 10_000 },
  )
  await shot(page, "files-conflict")
  await page.getByRole("button", { name: "Overwrite" }).click()
  await expect(page.getByText("This file changed on its machine")).toHaveCount(0)
  expect(f.files.get("docs/guide.md")?.content).toContain("Mine.")

  // An image opens as one.
  await openFile(page, ["img"], "logo.png")
  await expect(page.getByRole("img", { name: "img/logo.png" })).toHaveAttribute("src", /^blob:/)
  await shot(page, "files-image")

  // A new file is made in a folder from its menu, and opened.
  const t = await tree(page)
  await t.getByRole("button", { name: "Actions for src" }).click()
  await page.getByRole("menuitem", { name: "New file" }).click()
  await page.getByRole("textbox", { name: "Name" }).fill("util.ts")
  await shot(page, "files-new-file")
  await page.getByRole("button", { name: "Make" }).click()
  await expect(page.getByRole("tab", { name: "util.ts" })).toHaveAttribute("aria-selected", "true")
  expect(f.bodies.at(-1)).toEqual({ path: "src/util.ts", content: "" })

  // A tab with text not saved asks before it closes.
  await typeInEditor(page, "export {}")
  await page.getByRole("button", { name: "Close util.ts" }).click()
  await expect(page.getByRole("alertdialog", { name: "Close without saving?" })).toBeVisible()
  await page.getByRole("button", { name: "Keep editing" }).click()
  await expect(page.getByRole("alertdialog")).toHaveCount(0)
  await expect(page.getByRole("tab", { name: "util.ts" })).toBeVisible()

  // A file is deleted for good once the owner says so.
  await (await tree(page)).getByRole("button", { name: "Actions for README.md" }).click()
  await page.getByRole("menuitem", { name: "Delete" }).click()
  await expect(page.getByRole("alertdialog", { name: "Delete README.md?" })).toBeVisible()
  await shot(page, "files-delete")
  await page.getByRole("button", { name: "Delete", exact: true }).click()
  await expect(page.getByRole("alertdialog")).toHaveCount(0)
  await expect((await tree(page)).getByRole("treeitem", { name: "README.md" })).toHaveCount(0)
  expect(f.asked).toContain("DELETE file README.md")
})

// Scenario: the owner uploads files (issue #315). A file larger than a piece goes in pieces and lands in the folder;
// one whose name is taken asks, and is replaced over the file there when the owner says so; a file dropped on a
// folder goes into it.
test("the owner uploads files into a chat's folder", async ({ page }) => {
  test.setTimeout(90_000)
  const f = folder()
  await fakeServer(page, f)
  await page.goto("/chats/cht_1")

  // 900 KiB: three pieces of at most 384 KiB.
  const big = { name: "photo.jpg", mimeType: "image/jpeg", buffer: Buffer.alloc(900 << 10, 7) }
  await tree(page)
  await panel(page).getByLabel("Files to upload").setInputFiles(big)
  const uploads = panel(page).getByRole("list", { name: "Uploads" })
  await expect(uploads.getByText("Uploaded")).toBeVisible()
  await expect((await tree(page)).getByRole("treeitem", { name: "photo.jpg" })).toBeVisible()
  expect(f.asked.filter((a) => a.startsWith("POST uploads/chunk photo.jpg"))).toHaveLength(3)
  expect(f.files.get("photo.jpg")?.content).toBe(`${900 << 10} bytes`)
  await shot(page, "files-uploaded")

  // The same name again: asked, then replaced over the file there.
  await panel(page)
    .getByLabel("Files to upload")
    .setInputFiles({ ...big, buffer: Buffer.alloc(10, 1) })
  await expect(uploads.getByText("Replace the file there?")).toBeVisible()
  await shot(page, "files-upload-replace")
  await uploads.getByRole("button", { name: "Replace" }).click()
  await expect(uploads.getByText("Uploaded")).toHaveCount(1)
  expect(f.bodies.at(-1)).toMatchObject({ path: "photo.jpg", expect_sha256: "photo.jpg@1" })
  expect(f.files.get("photo.jpg")?.content).toBe("10 bytes")

  // A file dropped on a folder of the tree goes into it.
  // Made in the page, whose DataTransfer the test's own types do not know.
  const drop = await page.evaluateHandle(
    "(() => { const d = new DataTransfer(); d.items.add(new File(['# notes'], 'notes.md', { type: 'text/markdown' })); return d })()",
  )
  const docs = (await tree(page)).getByRole("treeitem", { name: "docs", exact: true })
  await docs.dispatchEvent("dragover", { dataTransfer: drop })
  await docs.dispatchEvent("drop", { dataTransfer: drop })
  await expect.poll(() => f.files.has("docs/notes.md")).toBe(true)
})
