import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Files panel of a chat's pane (issue #255), built, against a fake client API.

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

const workspaces = [
  {
    id: "bot",
    kind: "bot",
    name: "coordinator",
    root: "/home/coordinator/.hypomnema-coordinator/bots/coordinator",
    online: true,
  },
  {
    id: "tsk_2",
    kind: "task",
    name: "add a retry button",
    root: "/home/hyp/run_1/tsk_2",
    online: false,
    node_name: "kdlstc-dev",
  },
  {
    id: "tsk_1",
    kind: "task",
    name: "add GET /health",
    root: "/srv/hyp/run_1/tsk_1",
    online: true,
    node_name: "stc-hy-app",
  },
]

const files: Record<string, string> = {
  "src/app.ts":
    'import express from "express"\nimport { health } from "./routes/health"\n\nconst app = express()\napp.use(health)\n',
  "README.md": "# demo\n",
}

async function fakeServer(page: Page) {
  const asked: string[] = []

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const ws = /^\/chats\/cht_1\/workspaces\/([^/]+)\/(tree|file|changes|diff)$/.exec(path)
    if (ws) {
      const [, id, op] = ws
      const at = url.searchParams.get("path") ?? ""
      asked.push(`${id} ${op} ${at}`)

      if (op === "changes") {
        return id === "bot"
          ? json({ git: false, changes: [], truncated: false })
          : json({
              git: true,
              truncated: false,
              changes: [
                { path: "src/routes/health.ts", status: "created", modified_at: 3 },
                {
                  path: "src/app.ts",
                  status: "modified",
                  lines_added: 2,
                  lines_removed: 1,
                  modified_at: 2,
                },
                {
                  path: "src/old-ping.ts",
                  status: "deleted",
                  lines_added: 0,
                  lines_removed: 4,
                  modified_at: 0,
                },
              ],
            })
      }
      if (op === "tree") {
        const entries =
          at === ""
            ? [
                { name: "src", path: "src", type: "directory", bytes: 0, modified_at: 1 },
                { name: "README.md", path: "README.md", type: "file", bytes: 7, modified_at: 1 },
              ]
            : [{ name: "app.ts", path: "src/app.ts", type: "file", bytes: 90, modified_at: 1 }]

        return json({ entries, has_more: false })
      }
      if (op === "file") {
        const content = files[at] ?? ""

        return json({ path: at, bytes: content.length, binary: false, truncated: false, content })
      }

      if (at === "src/app.ts") {
        return json({
          path: at,
          binary: false,
          before: 'import express from "express"\n\nconst app = express()\n',
          after: files["src/app.ts"],
        })
      }

      return json({
        path: at,
        binary: false,
        after: "export const health = () => ({ ok: true })\n",
      })
    }

    switch (path) {
      case "/me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "/chats":
        return json({ data: [chat] })
      case "/chats/cht_1":
        return json(chat)
      case "/chats/cht_1/workspaces":
        return json({ data: workspaces })
      case "/chats/cht_1/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
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

  return asked
}

const panel = (page: Page) => page.getByRole("complementary", { name: "Files" })

// shown opens the panel when it is not: on a phone it gives way to a file it opened.
async function shown(page: Page) {
  if (!(await panel(page).isVisible())) await page.getByRole("button", { name: "Files" }).click()

  return panel(page)
}

// Scenario: the owner looks at what a chat's worker made. The Files button opens the panel on the newest Task's
// worktree, at what changed; a changed file opens as its diff in a tab of the pane. All files is the worktree as a
// tree, a directory read when it is opened, and a file opens as its text. A folder whose machine is offline says
// so, and the Bot's own folder, which is not in git, has no changes. The tabs opened stay after a reload.
test("the Files panel shows a chat's folders, their changes and files", async ({ page }) => {
  const asked = await fakeServer(page)
  await page.goto("/chats/cht_1")

  await page.getByRole("button", { name: "Files" }).click()
  await expect(panel(page).getByRole("combobox", { name: "Folder" })).toContainText(
    "add GET /health",
  )
  await expect(panel(page).getByText("/srv/hyp/run_1/tsk_1")).toBeVisible()

  const changed = panel(page).getByRole("list", { name: "Changed files" })
  await expect(changed.getByRole("button")).toHaveCount(3)
  await expect(changed.getByRole("button").nth(1)).toContainText("+2 −1")
  await shot(page, "files-changed")

  // A changed file opens as its diff; a new one as its text, since there is nothing to compare it with.
  await changed.getByRole("button", { name: /app\.ts/ }).click()
  await expect(page.getByRole("tab", { name: "app.ts" })).toHaveAttribute("aria-selected", "true")
  await expect(page.getByText("Changed since the last commit")).toBeVisible()
  await expect(page.locator('[data-testid="diff-view"]:visible .view-lines').last()).toContainText(
    "health",
  )
  // The added lines are marked once Monaco has worked the diff out.
  await expect(page.locator('[data-testid="diff-view"]:visible .line-insert').first()).toBeVisible()
  await shot(page, "files-diff")

  await (
    await shown(page)
  )
    .getByRole("list", { name: "Changed files" })
    .getByRole("button", { name: /health\.ts/ })
    .click()
  await expect(page.getByText("New file", { exact: true })).toBeVisible()
  await expect(page.locator('[data-testid="code-view"]:visible .view-lines')).toContainText(
    "ok: true",
  )

  await (await shown(page)).getByRole("button", { name: "All files" }).click()
  const tree = panel(page).getByRole("tree", { name: "All files" })
  await expect(tree.getByRole("treeitem")).toHaveText(["src", "README.md"])
  await tree.getByRole("treeitem", { name: "src" }).click()
  await tree.getByRole("treeitem", { name: "app.ts" }).click()
  // The file's text is a tab of its own, beside its diff.
  await expect(page.getByRole("tab", { name: "app.ts" }).last()).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await expect(page.locator('[data-testid="code-view"]:visible .view-lines')).toContainText(
    "express",
  )
  await shot(page, "files-tree-file")
  expect(asked).toEqual(
    expect.arrayContaining(["tsk_1 tree ", "tsk_1 tree src", "tsk_1 file src/app.ts"]),
  )

  // Another Task's worktree, on a machine that is offline, is not read.
  await (await shown(page)).getByRole("combobox", { name: "Folder" }).click()
  await page.getByRole("option", { name: /add a retry button/ }).click()
  await expect(panel(page)).toContainText("kdlstc-dev is offline")
  expect(asked.some((a) => a.startsWith("tsk_2"))).toBe(false)

  await panel(page).getByRole("combobox", { name: "Folder" }).click()
  await page.getByRole("option", { name: /coordinator/ }).click()
  await panel(page).getByRole("button", { name: "Changed" }).click()
  await expect(panel(page)).toContainText("not in a git repository")

  await page.reload()
  await expect(page.getByRole("tab")).toHaveText(["health page", "app.ts", "health.ts", "app.ts"])
  await (await shown(page)).getByRole("button", { name: "Close files" }).click()
  await expect(panel(page)).toHaveCount(0)
})
