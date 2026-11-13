import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Skills pages (issue #21), built, against a fake client API, on a phone and a desktop.

const ago = (hours: number) => new Date(Date.now() - hours * 3600_000).toISOString()

type Revision = {
  revision: number
  description: string
  digest: string
  size: number
  files: number
  source: string
  commit?: string
  created_at: string
}

type Skill = {
  name: string
  everyone: boolean
  latest: Revision
  revisions?: Revision[]
  created_at: string
  updated_at: string
}

const revision = (
  n: number,
  description: string,
  source: string,
  hours: number,
  commit?: string,
) => ({
  revision: n,
  description,
  digest: `${n}a1b2c3d4e5f6`.padEnd(64, "0"),
  size: 2048 * n + 300,
  files: n + 1,
  source,
  ...(commit ? { commit } : {}),
  created_at: ago(hours),
})

const reviewGo =
  "Review a Go change for correctness: errors wrapped with context, contexts passed, tests that name the contract."

function start(): Skill[] {
  return [
    {
      name: "review-go",
      everyone: true,
      latest: revision(
        3,
        reviewGo,
        "github:kl3inIT/agent-skills/review-go@main",
        2,
        "9f3c1a0be44d7e1c",
      ),
      revisions: [
        revision(3, reviewGo, "github:kl3inIT/agent-skills/review-go@main", 2, "9f3c1a0be44d7e1c"),
        revision(2, reviewGo, "github:kl3inIT/agent-skills/review-go@main", 50, "5d21e87aa0131234"),
        revision(1, reviewGo, "upload", 80),
      ],
      created_at: ago(80),
      updated_at: ago(2),
    },
    {
      name: "pdf",
      everyone: true,
      latest: revision(
        1,
        "Read, fill and merge PDF files: extract text and tables, fill forms, split and join pages.",
        "github:anthropics/skills/skills/pdf@HEAD",
        26,
        "2c5958ecac7c0000",
      ),
      created_at: ago(26),
      updated_at: ago(26),
    },
    {
      name: "lark-docs",
      everyone: false,
      latest: revision(
        2,
        "Write a weekly report as a Lark doc from the tasks closed this week, in the team's template.",
        "upload",
        72,
      ),
      created_at: ago(100),
      updated_at: ago(72),
    },
  ]
}

const builtin = [
  {
    name: "hypomnema-coordinator",
    description:
      "Plans a Hypomnema Run, has workers do its coding on the owner's nodes, and reports each Task's outcome.",
  },
  {
    name: "google-workspace",
    description:
      "Gmail, Calendar and Drive through gws, already signed in to the accounts the agent was given.",
  },
  {
    name: "lark",
    description:
      "Lark documents, sheets, Bases and messages through lark-cli, acting as the app the agent was given.",
  },
  {
    name: "superset",
    description: "Charts, dashboards and SQL on a Superset the agent was given an account for.",
  },
]

const namer = (name: string, skills: string[] = [], skills_off: string[] = []) => ({
  name,
  skills,
  skills_off,
})

async function fakeServer(page: Page) {
  let skills = start()
  const calls: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = decodeURIComponent(new URL(request.url()).pathname.replace("/api/v1", ""))
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const [, kind, name, sub] = path.split("/")

    if (kind === "skills" && request.method() !== "GET") calls.push(call)

    if (call === "GET /skill-search") {
      return json({
        exact: 3,
        data: [
          {
            id: "anthropics/skills/pdf",
            name: "pdf",
            source: "anthropics/skills",
            installs: 227931,
          },
          {
            id: "vercel-labs/json-render/react-pdf",
            name: "react-pdf",
            source: "vercel-labs/json-render",
            installs: 2376,
          },
          { id: "openai/skills/pdf", name: "pdf", source: "openai/skills", installs: 13788 },
          {
            id: "anthropics/knowledge-work-plugins/view-pdf",
            name: "view-pdf",
            source: "anthropics/knowledge-work-plugins",
            installs: 6400,
          },
        ],
      })
    }
    if (call === "GET /skill-audits") {
      return json({
        data: [
          { partner: "ath", risk: "safe" },
          { partner: "snyk", risk: "low" },
          { partner: "socket", risk: "safe", alerts: 0 },
        ],
      })
    }

    if (call === "POST /skills") {
      const body = request.postDataJSON() as { github: string; skill?: string }
      if (body.skill)
        return json(
          skills.find((s) => s.name === "pdf"),
          200,
        )
      if (!body.github.includes("pdf")) {
        return json(
          { code: "invalid", message: "invalid skill: no SKILL.md at the skill's root" },
          400,
        )
      }

      return json(
        skills.find((s) => s.name === "pdf"),
        200,
      )
    }

    if (kind === "skills" && name && name !== "archive") {
      const found = skills.find((s) => s.name === name)
      if (!found) return json({ code: "not_found", message: "not found" }, 404)

      if (sub === "revisions") {
        return json({
          data: [
            { path: "SKILL.md", size: 1840, executable: false },
            { path: "references/errors.md", size: 4100, executable: false },
            { path: "references/tests.md", size: 3200, executable: false },
            { path: "scripts/check.sh", size: 640, executable: true },
          ],
        })
      }
      if (sub === "everyone") {
        const body = request.postDataJSON() as { everyone: boolean }
        found.everyone = body.everyone

        return json(found)
      }
      if (request.method() === "DELETE") {
        if (name === "lark-docs") {
          return json(
            {
              code: "conflict",
              message: "bots vmap, reporter name skill lark-docs; take it out of them first",
            },
            409,
          )
        }
        skills = skills.filter((s) => s.name !== name)

        return route.fulfill({ status: 204 })
      }

      return json({ ...found, revisions: found.revisions ?? [found.latest] })
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /skills":
        return json({ data: skills })
      case "GET /built-in-skills":
        return json({ data: builtin })
      case "GET /profiles":
        return json({
          data: [namer("implementer"), namer("codex-worker"), namer("reviewer", [], ["review-go"])],
        })
      case "GET /bots":
        return json({
          data: [namer("vmap", ["lark-docs"]), namer("reporter", ["lark-docs"])],
        })
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

// Scenario: the owner opens Skills from Settings. Added and built-in skills are two sections of cards, then of
// tables; the search box narrows both.
test("the owner looks through the skills", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/vault")
  await page.getByRole("link", { name: "Skills" }).click()

  const review = page.getByRole("link", { name: "review-go" })
  await expect(review).toContainText("Everyone but reviewer")
  await expect(page.getByRole("link", { name: "lark-docs" })).toContainText("vmap, reporter")
  await expect(page.getByRole("article", { name: "lark" })).toContainText("Agents given a Lark app")
  expect(await fits(page)).toBe(true)
  await shot(page, "skills-list")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("row", { name: /review-go/ })).toBeVisible()
  expect(await fits(page)).toBe(true)
  await shot(page, "skills-table")

  await page.getByRole("button", { name: "grid" }).click()
  await page.getByLabel("Search skills").fill("lark")
  await expect(review).toHaveCount(0)
  await expect(page.getByRole("link", { name: "lark-docs" })).toBeVisible()
  await expect(page.getByRole("article", { name: "lark" })).toBeVisible()
})

// Scenario: the owner adds a skill from a repository with no SKILL.md and reads the server's words, then one that
// is fine and lands on its page; the Upload tab takes an archive.
test("the owner adds a skill", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/skills")

  await page.getByRole("button", { name: "Add skill" }).first().click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByLabel("Repository and folder").fill("kl3inIT/agent-skills")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(dialog).toContainText("no SKILL.md at the skill's root")
  await shot(page, "skills-add-github")

  await dialog.getByRole("tab", { name: "Upload" }).click()
  await dialog.locator("input[type=file]").setInputFiles({
    name: "lark-docs.zip",
    mimeType: "application/zip",
    buffer: Buffer.alloc(4096),
  })
  await expect(dialog).toContainText("lark-docs.zip")
  await shot(page, "skills-add-upload")

  await dialog.getByRole("tab", { name: "GitHub" }).click()
  await dialog.getByLabel("Repository and folder").fill("anthropics/skills/skills/pdf")
  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/skills\/pdf$/)
  await expect(page.getByRole("heading", { level: 1, name: "pdf", exact: true })).toBeVisible()
  expect(calls).toEqual(["POST /skills", "POST /skills"])
})

// Scenario: the owner searches skills.sh for "pdf", picks the first result, reads its audits and adds it by name.
test("the owner adds a skill found on skills.sh", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/skills")

  await page.getByRole("button", { name: "Add skill" }).first().click()
  const dialog = page.locator("[data-slot=dialog-content]")
  await dialog.getByRole("tab", { name: "skills.sh" }).click()
  await shot(page, "skills-sh-empty")
  await dialog.getByLabel("Search skills.sh").fill("pdf")
  const found = dialog.getByRole("list", { name: "Skills found" })
  await expect(found.getByRole("button", { name: /anthropics\/skills/ })).toContainText(
    "227.9K installs",
  )
  await expect(found.getByRole("button", { name: /^pdf anthropics\/skills/ })).toContainText(
    "Added",
  )

  await found.getByRole("button", { name: /^pdf anthropics\/skills/ }).click()
  await expect(found).toContainText("Gen Agent Trust Hub")
  expect(await fits(page)).toBe(true)
  await shot(page, "skills-sh-picked")

  await dialog.getByRole("button", { name: "Add", exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/skills\/pdf$/)
  expect(calls).toEqual(["POST /skills"])
})

// Scenario: on a skill's page the owner reads its files and revisions, takes it from everyone, and is refused
// removing a skill bots still name.
test("the owner manages one skill", async ({ page }) => {
  const calls = await fakeServer(page)
  await page.goto("/settings/skills/review-go")

  await expect(page.getByRole("list", { name: "Skill files" })).toContainText("check.sh")
  await expect(page.getByTestId("revision")).toHaveCount(3)
  await expect(page.getByRole("region", { name: "Used by" })).toContainText("reviewer")
  expect(await fits(page)).toBe(true)
  await shot(page, "skill-page")

  await page.getByRole("switch", { name: "For every worker and bot" }).click()
  await expect(page.getByRole("switch", { name: "For every worker and bot" })).not.toBeChecked()
  await expect(page.getByRole("region", { name: "Used by" })).toContainText("No one yet")

  await page.goto("/settings/skills/lark-docs")
  await page.getByRole("button", { name: "Remove" }).click()
  const dialog = page.getByRole("alertdialog")
  await dialog.getByRole("button", { name: "Remove" }).click()
  await expect(dialog).toContainText("bots vmap, reporter name skill lark-docs")
  await shot(page, "skill-remove-refused")

  expect(calls).toEqual(["PUT /skills/review-go/everyone", "DELETE /skills/lark-docs"])
})
