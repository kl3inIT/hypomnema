import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { Skill, SkillRevision } from "@/api/generated/types.gen"

import { skillFileTree } from "./file-tree"
import { SkillScreen } from "./skill-screen"
import { SkillsScreen } from "./skills-screen"
import { auditResult, installsLabel, isAdded, sourceLine, usersLine, usersOf } from "./skills-state"

beforeEach(() => {
  // The router scrolls to the top when it opens a page; jsdom has no scrolling.
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const now = new Date().toISOString()

const revision = (n: number, m: Partial<SkillRevision> = {}): SkillRevision => ({
  revision: n,
  description: "Review a Go change for correctness.",
  digest: `${n}`.repeat(64),
  size: 2048,
  files: 2,
  source: "upload",
  created_at: now,
  ...m,
})

const skill = (name: string, m: Partial<Skill> = {}): Skill => ({
  name,
  everyone: false,
  latest: revision(1),
  created_at: now,
  updated_at: now,
  ...m,
})

const github = revision(2, {
  source: "github:kl3inIT/agent-skills/review-go@main",
  commit: "9f3c1a0be44d7e1c",
})

const profile = (name: string, skills: string[] = [], skills_off: string[] = []) => ({
  name,
  skills,
  skills_off,
})

// The server: the skills, the built-in ones, and the agents and bots that name them. Every request that changes
// something is kept; refuse names requests it turns down, with the server's words.
function server(
  skills: Skill[],
  refuse: Record<string, [number, string]> = {},
  namers = {
    profiles: [profile("implementer"), profile("reviewer", [], ["review-go"])],
    bots: [profile("vmap", ["lark-docs"])],
  },
) {
  let list = skills
  const requests: { call: string; body: unknown }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = decodeURIComponent(new URL(request.url).pathname.replace("/api/v1", ""))
    const call = `${request.method} ${path}`

    if (call === "GET /skills") return json({ data: list })
    if (call === "GET /profiles") return json({ data: namers.profiles })
    if (call === "GET /bots") return json({ data: namers.bots })
    if (call === "GET /skill-search") {
      return json({
        exact: 1,
        data: [
          {
            id: "anthropics/skills/pdf",
            name: "pdf",
            source: "anthropics/skills",
            installs: 227931,
          },
          { id: "o/r/review-go", name: "review-go", source: "o/r", installs: 1 },
        ],
      })
    }
    if (call === "GET /skill-audits") {
      return json({
        data: [
          { partner: "ath", risk: "safe" },
          { partner: "socket", risk: "low", alerts: 2 },
          { partner: "snyk", risk: "high" },
        ],
      })
    }
    if (call === "GET /built-in-skills") {
      return json({
        data: [{ name: "lark", description: "Lark documents through lark-cli." }],
      })
    }
    if (request.method === "GET" && path.endsWith("/files")) {
      return json({
        data: [
          { path: "SKILL.md", size: 900, executable: false },
          { path: "scripts/check.sh", size: 120, executable: true },
        ],
      })
    }

    const body = (await request
      .clone()
      .json()
      .catch(() => undefined)) as unknown
    if (request.method !== "GET") requests.push({ call, body })

    const refused = refuse[call]
    if (refused) return json({ code: "refused", message: refused[1] }, refused[0])

    const [, , name, sub] = path.split("/")
    const found = list.find((s) => s.name === name)

    if (call === "POST /skills") {
      const added = skill("pdf", {
        latest: revision(1, { source: "github:anthropics/skills/skills/pdf@HEAD" }),
      })
      list = [...list, added]

      return json(added, 201)
    }
    if (!found) return json({ code: "not_found", message: "not found" }, 404)
    if (request.method === "GET") {
      return json({ ...found, revisions: [found.latest, revision(found.latest.revision - 1)] })
    }
    if (sub === "everyone") {
      const changed = { ...found, everyone: (body as { everyone: boolean }).everyone }
      list = list.map((s) => (s.name === name ? changed : s))

      return json(changed)
    }
    if (request.method === "DELETE") {
      list = list.filter((s) => s.name !== name)

      return new Response(null, { status: 204 })
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return requests
}

function open(path: string) {
  const root = createRootRoute()
  const list = createRoute({
    getParentRoute: () => root,
    path: "/settings/skills",
    component: SkillsScreen,
  })
  const one = createRoute({
    getParentRoute: () => root,
    path: "/settings/skills/$name",
    component: function One() {
      const { name } = one.useParams()

      return <SkillScreen key={name} name={name} />
    },
  })
  const router = createRouter({
    routeTree: root.addChildren([list, one]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )

  return router
}

// Scenario: two skills added, one for everyone but an agent that turns it off, one an agent adds; and a built-in
// one. Each card says where its newest revision came from and who gets it.
test("each skill says where it came from and who gets it", async () => {
  server([skill("review-go", { everyone: true, latest: github }), skill("lark-docs")])
  open("/settings/skills")

  const review = await screen.findByRole("link", { name: "review-go" })
  expect(review).toHaveTextContent("kl3inIT/agent-skills/review-go@main")
  expect(review).toHaveTextContent("For everyone")
  expect(await within(review).findByText("Everyone but reviewer")).toBeInTheDocument()

  const docs = screen.getByRole("link", { name: "lark-docs" })
  expect(docs).toHaveTextContent("Uploaded")
  expect(docs).toHaveTextContent("vmap")
  expect(docs).not.toHaveTextContent("For everyone")

  const builtin = await screen.findByRole("article", { name: "lark" })
  expect(builtin).toHaveTextContent("Agents given a Lark app")
})

// Scenario: the owner adds a skill from GitHub. The server is asked for that repository, and the page opens the
// new skill.
test("a skill added from GitHub opens its page", async () => {
  const user = userEvent.setup()
  const requests = server([])
  const router = open("/settings/skills")

  await user.click((await screen.findAllByRole("button", { name: "Add skill" }))[0]!)
  await user.type(screen.getByLabelText("Repository and folder"), "anthropics/skills/skills/pdf")
  await user.click(screen.getByRole("button", { name: "Add" }))

  expect(await screen.findByRole("heading", { name: "pdf" })).toBeInTheDocument()
  expect(router.state.location.pathname).toBe("/settings/skills/pdf")
  expect(requests).toEqual([
    { call: "POST /skills", body: { github: "anthropics/skills/skills/pdf" } },
  ])
})

// Scenario: on a skill's page the owner gives it to everyone; the files of its newest revision and its revisions
// are listed.
test("a skill is given to everyone from its page", async () => {
  const user = userEvent.setup()
  const requests = server([skill("review-go", { latest: github })])
  open("/settings/skills/review-go")

  expect(await screen.findByRole("list", { name: "Skill files" })).toHaveTextContent("check.sh")
  expect(screen.getAllByTestId("revision")).toHaveLength(2)
  expect(screen.getByRole("button", { name: "Update from GitHub" })).toBeInTheDocument()

  await user.click(screen.getByRole("switch", { name: "For every worker and bot" }))

  expect(await screen.findByText("For everyone")).toBeInTheDocument()
  expect(requests).toEqual([{ call: "PUT /skills/review-go/everyone", body: { everyone: true } }])
})

// Scenario: an agent still names the skill, so the server refuses to remove it. The dialog stays open with the
// server's words, and the skill stays.
test("a refused removal says why and keeps the skill", async () => {
  const user = userEvent.setup()
  const refusal = "bots vmap name skill lark-docs; take it out of them first"
  server([skill("lark-docs", { latest: revision(2) })], {
    "DELETE /skills/lark-docs": [409, refusal],
  })
  const router = open("/settings/skills/lark-docs")

  await user.click(await screen.findByRole("button", { name: "Remove" }))
  const dialog = await screen.findByRole("alertdialog")
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))

  expect(await within(dialog).findByText(refusal)).toBeInTheDocument()
  expect(router.state.location.pathname).toBe("/settings/skills/lark-docs")
})

// Scenario: the owner searches skills.sh from Add skill, picks a result and reads what its auditors said, then adds
// it by its repository and name. A skill already here is marked.
test("a skill found on skills.sh is added by its name", async () => {
  const user = userEvent.setup()
  const requests = server([
    skill("review-go", { latest: revision(1, { source: "github:o/r/skills/review-go@main" }) }),
  ])
  const router = open("/settings/skills")

  await user.click((await screen.findAllByRole("button", { name: "Add skill" }))[0]!)
  await user.click(screen.getByRole("tab", { name: "skills.sh" }))
  expect(screen.getByRole("button", { name: "Add" })).toBeDisabled()
  await user.type(screen.getByLabelText("Search skills.sh"), "pdf")

  const found = await screen.findByRole("list", { name: "Skills found" })
  const pdf = within(found).getByRole("button", { name: /pdf/ })
  expect(pdf).toHaveTextContent("227.9K installs")
  expect(within(found).getByRole("button", { name: /review-go/ })).toHaveTextContent("Added")

  await user.click(pdf)
  expect(await within(found).findByText("High Risk")).toBeInTheDocument()
  expect(within(found).getByText("2 alerts")).toBeInTheDocument()
  expect(within(found).getByRole("link", { name: /skills\.sh/ })).toHaveAttribute(
    "href",
    "https://skills.sh/anthropics/skills/pdf",
  )

  await user.click(screen.getByRole("button", { name: "Add" }))
  expect(await screen.findByRole("heading", { level: 1, name: "pdf" })).toBeInTheDocument()
  expect(router.state.location.pathname).toBe("/settings/skills/pdf")
  expect(requests).toEqual([
    { call: "POST /skills", body: { github: "anthropics/skills", skill: "pdf" } },
  ])
})

test("a result is here only when its name and repository are", () => {
  const github = (where: string) => revision(1, { source: `github:${where}@HEAD` })
  const result = { name: "pdf", source: "anthropics/skills" }

  expect(isAdded(result, [skill("pdf", { latest: github("anthropics/skills/skills/pdf") })])).toBe(
    true,
  )
  expect(isAdded(result, [skill("pdf", { latest: github("Anthropics/Skills/pdf") })])).toBe(true)
  expect(isAdded(result, [skill("pdf", { latest: github("openai/skills/skills/pdf") })])).toBe(
    false,
  )
  expect(isAdded(result, [skill("pdf", { latest: github("anthropics/skills-extra/pdf") })])).toBe(
    false,
  )
  expect(isAdded(result, [skill("pdf", { latest: github("anthropics/skills") })])).toBe(true)
  expect(isAdded(result, [skill("pdf")])).toBe(false)
  expect(isAdded(result, [skill("pdf-tools", { latest: github("anthropics/skills/pdf") })])).toBe(
    false,
  )
})

test("installs and audits are said as the skills CLI says them", () => {
  expect(installsLabel(227931)).toBe("227.9K installs")
  expect(installsLabel(2_000_000)).toBe("2M installs")
  expect(installsLabel(1)).toBe("1 install")
  expect(installsLabel(0)).toBe("")
  expect(auditResult({ partner: "socket", risk: "safe", alerts: 0 })).toEqual({
    text: "0 alerts",
    tone: "good",
  })
  expect(auditResult({ partner: "snyk", risk: "medium" })).toEqual({
    text: "Med Risk",
    tone: "warn",
  })
  expect(auditResult({ partner: "ath", risk: "unknown" })).toEqual({ text: "--", tone: "none" })
})

test("who gets a skill is said in a few words", () => {
  const namers = [profile("a", ["x"]), profile("b", [], ["x"])]

  expect(usersLine(usersOf({ name: "x", everyone: true }, namers, []))).toBe("Everyone but b")
  expect(usersLine(usersOf({ name: "x", everyone: false }, namers, []))).toBe("a")
  expect(usersLine(usersOf({ name: "y", everyone: false }, namers, []))).toBe("No one yet")
})

test("a source names its repository and the ref it was asked for", () => {
  expect(sourceLine({ source: "github:o/r/path@v1" })).toBe("o/r/path@v1")
  expect(sourceLine({ source: "github:o/r@HEAD" })).toBe("o/r")
  expect(sourceLine({ source: "upload" })).toBe("Uploaded")
})

test("files nest into folders, folders first", () => {
  expect(skillFileTree(["SKILL.md", "scripts/b.sh", "scripts/a.sh"])).toEqual([
    {
      name: "scripts",
      path: "scripts",
      children: [
        { name: "a.sh", path: "scripts/a.sh" },
        { name: "b.sh", path: "scripts/b.sh" },
      ],
    },
    { name: "SKILL.md", path: "SKILL.md" },
  ])
})
