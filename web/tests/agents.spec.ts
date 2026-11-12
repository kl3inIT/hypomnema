import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Agents pages, built, against a fake client API, on a phone and a desktop.

const hours = (n: number) => new Date(Date.now() - n * 3_600_000).toISOString()

const profile = (name: string, m: object = {}) => ({
  name,
  description: "",
  version_id: `pfv_${name}`,
  version: 1,
  runtime: "claude",
  fallbacks: [],
  source: `name: ${name}\nruntime: claude\n`,
  created_at: hours(30),
  available: true,
  ...m,
})

const reviewerSource =
  "name: reviewer\n" +
  "description: >-\n  Reviews a change against its spec and reports problems with file and line; changes files only when the task asks for fixes.\n" +
  "runtime: claude\nmodel:\n  name: opus\n  effort: xhigh\n" +
  "instruction: |\n  Review the change the task names. Report what is wrong or missing, with file and line, as the summary of\n  `finish`; change files only when the task asks for fixes.\n"

const reviewer = profile("reviewer", {
  description:
    "Reviews a change against its spec and reports problems with file and line; changes files only when the task asks for fixes.",
  version: 3,
  version_id: "pfv_reviewer_3",
  model: "opus",
  provider: "claude-subscription",
  effort: "xhigh",
  instruction:
    "Review the change the task names. Report what is wrong or missing, with file and line, as the summary of\n`finish`; change files only when the task asks for fixes.\n",
  source: reviewerSource,
  created_at: hours(2),
  available: false,
  unavailable_reason: "accounts_cooling",
  available_at: new Date(Date.now() + 95 * 60_000).toISOString(),
  fallbacks: ["codex-implementer"],
})

const models = [
  { provider: "claude-subscription", name: "opus", kind: "claude_subscription" },
  { provider: "claude-subscription", name: "sonnet", kind: "claude_subscription" },
  { provider: "gateway", name: "gpt-sol", kind: "model_gateway" },
  { provider: "gateway", name: "gpt-luna", kind: "model_gateway" },
]

async function fakeServer(page: Page) {
  let profiles = [
    profile("implementer", {
      description:
        "Makes a change in the repository, runs its checks and opens a pull request. The default for any change.",
      version: 2,
      created_at: hours(20),
    }),
    profile("codex-implementer", {
      description:
        "Makes a narrow, well-scoped change in the repository, runs its checks and opens a pull request. Runs on Codex; use implementer for work across many files, refactors and writing tests.",
      runtime: "codex",
      model: "gpt-sol",
      provider: "gateway",
      effort: "high",
      available: false,
      unavailable_reason: "no_node",
      created_at: hours(1),
    }),
    reviewer,
    profile("explorer", {
      description:
        "Investigates code or a question and reports what it found, with file paths; changes nothing.",
      model: "sonnet",
      provider: "claude-subscription",
      effort: "low",
      version: 2,
    }),
  ]
  const applied: { name: string; source: string }[] = []

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const one = /^\/profiles\/([^/]+)$/.exec(path)
    if (one && request.method() === "GET") {
      const found = profiles.find((p) => p.name === one[1])

      return found ? json(found) : json({ code: "not_found", message: "no such profile" }, 404)
    }

    if (one && request.method() === "PUT") {
      const { source } = request.postDataJSON() as { source: string }
      applied.push({ name: one[1]!, source })

      if (source.includes("gpt-sol") && source.includes("runtime: claude")) {
        return json(
          {
            code: "invalid",
            message: "claude cannot run against the provider gateway (model_gateway)",
          },
          422,
        )
      }

      const before = profiles.find((p) => p.name === one[1])
      const stored = {
        ...(before ??
          profile(one[1]!, { description: "Reviews a change, on Codex.", runtime: "codex" })),
        version: (before?.version ?? 0) + 1,
        version_id: `pfv_${one[1]}_new`,
        source,
        created_at: new Date().toISOString(),
      }
      profiles = [...profiles.filter((p) => p.name !== one[1]), stored]

      return json(stored, 201)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /profiles":
        return json({ data: profiles })
      case "GET /models":
        return json({ data: models })
      case "GET /profiles/reviewer/versions":
        return json({
          data: [
            reviewer,
            {
              ...reviewer,
              version: 2,
              version_id: "pfv_reviewer_2",
              created_at: hours(50),
              source: reviewerSource.replace("  effort: xhigh\n", ""),
            },
            {
              ...reviewer,
              version: 1,
              version_id: "pfv_reviewer_1",
              created_at: hours(200),
              source: "name: reviewer\nruntime: claude\n",
            },
          ],
        })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /chats":
      case "GET /questions":
        return json({ data: [] })
      default:
        return json({ code: "not_found", message: "not found" }, 404)
    }
  })

  return applied
}

// Scenario: the owner looks at the agents as cards, then as a table that scrolls inside its box.
test("the agents show as cards and as a table, each saying whether it can run", async ({
  page,
}) => {
  await fakeServer(page)
  await page.goto("/agents")

  await expect(page.getByText("2 can run · 2 cannot")).toBeVisible()
  await expect(page.getByRole("article", { name: "codex-implementer" })).toContainText(
    "No machine online has Codex",
  )
  await expect(page.getByRole("article", { name: "reviewer" })).toContainText(
    /Claude Code accounts wait until/,
  )
  await expect(page.getByRole("article", { name: "implementer", exact: true })).toContainText(
    "Can run now",
  )
  // The agent that takes its Tasks while it is out of quota (issue #171).
  await expect(page.getByRole("article", { name: "reviewer" })).toContainText(
    "Fallback:codex-implementer",
  )
  await shot(page, "agents-grid")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("row", { name: /explorer/ })).toContainText("Low")
  await shot(page, "agents-table")
  // The page itself does not scroll sideways: the table does, inside its box.
  expect(await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")).toBe(
    true,
  )
})

// Scenario: the owner opens an agent, reads its fields, looks at the YAML and at its versions.
test("an agent opens to its builder, its YAML and its versions", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/agents")
  await page.getByRole("link", { name: "reviewer" }).click()

  await expect(page.getByRole("heading", { name: "reviewer", level: 2 })).toBeVisible()
  await expect(page.getByText("Version 3 · Claude Code")).toBeVisible()
  await expect(page.getByLabel("Name")).toHaveAttribute("readonly", "")
  await expect(page.getByLabel("Model", { exact: true })).toContainText("opus")
  await expect(page.getByLabel("Model", { exact: true })).toContainText("claude-subscription")
  await expect(
    page.getByRole("group", { name: "Effort" }).getByRole("button", { name: "Extra High" }),
  ).toHaveAttribute("aria-pressed", "true")
  await expect(page.getByRole("button", { name: "Save revision" })).toBeDisabled()
  await shot(page, "agent-builder")

  // Its fallbacks, in order; another worker is added and the list is one longer.
  await expect(page.getByLabel("Fallback 1")).toHaveValue("codex-implementer")
  await page.getByLabel("Fallbacks", { exact: true }).selectOption("explorer")
  await expect(page.getByLabel("Fallback 2")).toHaveValue("explorer")
  await page.getByLabel("Fallback 2").scrollIntoViewIfNeeded()
  await shot(page, "agent-fallbacks")
  await page.getByRole("button", { name: "Remove fallback explorer" }).click()
  await expect(page.getByRole("button", { name: "Save revision" })).toBeDisabled()

  await page.getByRole("button", { name: "YAML" }).click()
  // The YAML is in an editor that knows the profile's shape: a key it does not have is marked as it is typed.
  const yaml = page.getByTestId("yaml-editor")
  await expect(yaml.locator(".view-lines")).toContainText(/name:\sreviewer/)
  await expect(yaml.locator(".squiggly-error, .squiggly-warning")).toHaveCount(0)
  await shot(page, "agent-yaml")
  await yaml.locator(".view-lines").click()
  await page.keyboard.press("Control+End")
  await page.keyboard.type("nope: 1")
  await expect(yaml.locator(".squiggly-error, .squiggly-warning").first()).toBeVisible()
  await shot(page, "agent-yaml-error")

  await page.getByRole("button", { name: "versions" }).click()
  await page.getByText("Version 2", { exact: true }).click()
  await expect(page.getByRole("button", { name: "Restore this version" })).toBeVisible()
  await shot(page, "agent-versions")
})

// Scenario: a change the server refuses stays on the page with the server's reason; a change it takes becomes
// the next version.
test("a refused save says why, and a saved one becomes the next version", async ({ page }) => {
  const applied = await fakeServer(page)
  await page.goto("/agents/reviewer")

  // The model's picker: every provider's models under the provider's name, and a search that narrows them.
  await page.getByLabel("Model", { exact: true }).click()
  const list = page.getByRole("listbox")
  await expect(list.getByRole("option")).toHaveCount(5)
  await shot(page, "agent-model-open")
  await page.getByLabel("Search models").fill("nothing like this")
  await expect(page.getByText("No models found.")).toBeVisible()
  await shot(page, "agent-model-none")
  await page.getByLabel("Search models").fill("gate sol")
  await expect(list.getByRole("option")).toHaveText(["gpt-sol"])
  await shot(page, "agent-model-search")
  await page.keyboard.press("Enter")
  await expect(page.getByLabel("Model", { exact: true })).toContainText("gpt-sol")
  await page.getByRole("button", { name: "Save revision" }).click()
  await expect(
    page.getByText("claude cannot run against the provider gateway (model_gateway)"),
  ).toBeVisible()
  await shot(page, "agent-refused")

  await page.getByLabel("Model", { exact: true }).click()
  await page.getByRole("option", { name: "sonnet" }).click()
  await page.getByRole("button", { name: "Save revision" }).click()
  await expect(page.getByText("Version 4 · Claude Code")).toBeVisible()
  expect(applied.at(-1)?.source).toContain("  name: sonnet\n")
})

// Scenario: the owner makes a new agent on Codex.
test("a new agent is made from the builder", async ({ page }) => {
  const applied = await fakeServer(page)
  await page.goto("/agents")
  await page.getByRole("link", { name: "New agent" }).click()

  await page.getByLabel("Name").fill("codex-reviewer")
  await page.getByLabel("Description").fill("Reviews a change made on Claude Code, on Codex.")
  await page.getByLabel("Runs on").selectOption("codex")
  await page.getByLabel("Model", { exact: true }).click()
  await page.getByRole("option", { name: "gpt-sol" }).click()
  await page
    .getByRole("group", { name: "Effort" })
    .getByRole("button", { name: "Extra High" })
    .click()
  await shot(page, "agent-new")

  await page.getByRole("button", { name: "Create profile" }).click()
  await expect(page.getByRole("heading", { name: "codex-reviewer", level: 2 })).toBeVisible()
  expect(applied[0]?.name).toBe("codex-reviewer")
})

// Scenario: the owner makes a second agent like one that exists.
test("an agent is duplicated", async ({ page }) => {
  const applied = await fakeServer(page)
  await page.goto("/agents/reviewer")

  await page.getByRole("link", { name: "Duplicate" }).click()
  await expect(page.getByLabel("Name")).toHaveValue("reviewer-copy")
  await expect(page.getByLabel("Model", { exact: true })).toContainText("opus")
  await shot(page, "agent-duplicate")

  await page.getByRole("button", { name: "Create profile" }).click()
  await expect(page.getByRole("heading", { name: "reviewer-copy", level: 2 })).toBeVisible()
  expect(applied.at(-1)?.name).toBe("reviewer-copy")
})
