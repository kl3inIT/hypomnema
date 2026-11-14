import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { choose, options } from "@/test/choose"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type {
  Bot,
  BotSettings,
  Integration,
  Model,
  Profile,
  Skill,
} from "@/api/generated/types.gen"

import { BotScreen, NewBotScreen } from "./bot-screen"
import { BotsScreen } from "./bots-screen"
import { emptyDraft, memoryEntries, memoryUsed, problem, settingsOf } from "./bots-state"

beforeEach(() => {
  window.localStorage.clear()
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })

const bot = (name: string, m: Partial<Bot> = {}): Bot => ({
  name,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  created_at: new Date(Date.now() - 48 * 3_600_000).toISOString(),
  updated_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
  ...m,
})

const models: Model[] = [
  { provider: "claude-subscription", name: "opus", kind: "claude_subscription" },
  { provider: "gateway", name: "gpt-luna", kind: "model_gateway" },
]

const skill = (name: string, everyone = false): Skill => ({
  name,
  everyone,
  latest: {
    revision: 1,
    description: `What ${name} teaches.`,
    digest: "d",
    size: 1,
    files: 1,
    source: "upload",
    created_at: new Date(0).toISOString(),
  },
  created_at: new Date(0).toISOString(),
  updated_at: new Date(0).toISOString(),
})

// The server: the Bots, the models and the skills. A PUT stores what it is given and keeps the memory; a DELETE
// answers as `remove` says, or takes the Bot away. It keeps what was put and what was removed.
function server(
  start: Bot[],
  opts: {
    remove?: (name: string) => Response | undefined
    skills?: Skill[]
    integrations?: Integration[]
    profiles?: Profile[]
    // hold keeps a PUT unanswered until it resolves.
    hold?: Promise<void>
  } = {},
) {
  let bots = start
  const put: { name: string; settings: BotSettings }[] = []
  const removed: string[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")

    if (request.method === "GET" && path === "/bots") return json({ data: bots })
    if (request.method === "GET" && path === "/models") return json({ data: models })
    if (request.method === "GET" && path === "/skills") return json({ data: opts.skills ?? [] })
    if (request.method === "GET" && path === "/integrations")
      return json({ data: opts.integrations ?? [] })
    if (request.method === "GET" && path === "/profiles") return json({ data: opts.profiles ?? [] })

    const one = /^\/bots\/([^/]+)$/.exec(path)
    const found = one && bots.find((b) => b.name === one[1])

    if (request.method === "GET" && one) {
      return found ? json(found) : json({ code: "not_found", message: "no such bot" }, 404)
    }

    if (request.method === "PUT" && one) {
      const settings = (await request.clone().json()) as BotSettings
      put.push({ name: one[1]!, settings })
      await opts.hold

      const { provider: _provider, model: _model, effort: _effort, ...kept } = found ?? bot(one[1]!)
      const stored: Bot = { ...kept, ...settings, persona: settings.persona ?? "" }
      bots = [...bots.filter((b) => b.name !== one[1]), stored]

      return json(stored)
    }

    if (request.method === "DELETE" && one) {
      const refused = opts.remove?.(one[1]!)
      if (refused) return refused

      removed.push(one[1]!)
      bots = bots.filter((b) => b.name !== one[1])

      return new Response(null, { status: 204 })
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return { put, removed }
}

function open(path: string) {
  const root = createRootRoute({ component: Outlet })
  const routes = [
    createRoute({ getParentRoute: () => root, path: "/bots", component: BotsScreen }),
    createRoute({ getParentRoute: () => root, path: "/bots/new", component: NewBotScreen }),
    createRoute({
      getParentRoute: () => root,
      path: "/bots/$name",
      component: function One() {
        const { name } = routes[2]!.useParams() as { name: string }

        return <BotScreen key={name} name={name} />
      },
    }),
    createRoute({ getParentRoute: () => root, path: "/", component: () => <p>Start</p> }),
  ]
  const router = createRouter({
    routeTree: root.addChildren(routes),
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

// The memory is counted as the server counts it: a character a code point, the newlines between entries too.
test("the memory's size is counted in code points", () => {
  expect(memoryUsed("")).toBe(0)
  expect(memoryUsed("ab\ncd")).toBe(5)
  expect(memoryUsed("Tiếng Việt 👍")).toBe(12)
})

test("the memory is read as one entry a line, without the empty ones", () => {
  expect(memoryEntries("")).toEqual([])
  expect(memoryEntries("The owner writes in Vietnamese.\n\nDeploys go out on Fridays.\n")).toEqual([
    "The owner writes in Vietnamese.",
    "Deploys go out on Fridays.",
  ])
})

// A save replaces the model, the effort and the persona together, so what is empty is left out, not sent empty.
// The skills are always sent: an empty list takes the Bot's skills away (issue #21).
test("a draft is sent as the settings the server replaces", () => {
  expect(settingsOf({ ...emptyDraft, name: "helper", persona: "  Be brief.\n" }, models)).toEqual({
    persona: "Be brief.",
    skills: [],
    skills_off: [],
  })
  expect(
    settingsOf(
      {
        name: "helper",
        model: "gateway/gpt-luna",
        effort: "high",
        persona: "Be brief.",
        skills: ["review-go"],
        skillsOff: ["pdf"],
      },
      models,
    ),
  ).toEqual({
    provider: "gateway",
    model: "gpt-luna",
    effort: "high",
    persona: "Be brief.",
    skills: ["review-go"],
    skills_off: ["pdf"],
  })
})

test("a new Bot's name follows the server's rule and is not one taken", () => {
  const named = (name: string) => problem({ ...emptyDraft, name }, ["coordinator"])

  expect(named("")).toBe("Give the Bot a name.")
  expect(named("Researcher")).toMatch(/lowercase letters/)
  expect(named("1st")).toMatch(/starting with a letter/)
  expect(named("a".repeat(64))).toMatch(/at most 63/)
  expect(named("coordinator")).toBe("A Bot has this name already.")
  expect(named("research-2")).toBeNull()
})

// Scenario: two Bots. Each card says its model, or that pi chooses, and how full its memory is.
test("a card says the Bot's model and how much memory it uses", async () => {
  server([
    bot("coordinator", { memory: "The owner writes in Vietnamese.\nDeploys go out on Fridays." }),
    bot("researcher", {
      provider: "gateway",
      model: "gpt-luna",
      effort: "high",
      persona: "You are the team's researcher.",
    }),
  ])
  open("/bots")

  const first = await screen.findByRole("article", { name: "coordinator" })
  expect(first).toHaveTextContent("No persona.")
  expect(first).toHaveTextContent("Model:pi's default model")
  expect(first).toHaveTextContent("Memory:58 of 2,200 characters")

  const second = screen.getByRole("article", { name: "researcher" })
  expect(second).toHaveTextContent("You are the team's researcher.")
  expect(second).toHaveTextContent("Model:gpt-luna(gateway)")
  expect(second).toHaveTextContent("Effort:High")
  expect(second).toHaveTextContent("Memory:0 of 2,200 characters")
})

// Scenario: the owner asked to see on each card what a Bot works with (2026-11-13). The coordinator has no list,
// so it works with everything switched on; superset-report is limited to two Connections, named with their
// Integration's name; a Bot limited to none says so.
test("a card says which Connections the Bot works with", async () => {
  const service = (id: string, name: string): Integration => ({
    id,
    name,
    kind: "app",
    services: [],
    connections: [],
  })
  server(
    [
      bot("coordinator"),
      bot("superset-report", {
        connections: { limited: true, names: ["superset:prod", "lark:report"] },
      }),
      bot("quiet", { connections: { limited: true, names: [] } }),
    ],
    { integrations: [service("superset", "Superset"), service("lark", "Lark")] },
  )
  open("/bots")

  expect(await screen.findByRole("article", { name: "coordinator" })).toHaveTextContent(
    "Works with:Everything you switched on",
  )
  const report = screen.getByRole("article", { name: "superset-report" })
  expect(
    within(report)
      .getAllByRole("listitem")
      .map((item) => item.textContent),
  ).toEqual(["Superset · prod", "Lark · report"])
  expect(screen.getByRole("article", { name: "quiet" })).toHaveTextContent("Works with:No account")
})

// Scenario: the owner prefers the table; the choice is kept in this browser, and the table says the same things.
test("the table shows the same Bots and the layout is remembered", async () => {
  server([bot("coordinator", { memory: "One entry." })])
  open("/bots")
  const user = userEvent.setup()

  await screen.findByRole("article", { name: "coordinator" })
  await user.click(screen.getByRole("button", { name: "table" }))

  expect(screen.getByRole("row", { name: /coordinator/ })).toHaveTextContent(
    "10 of 2,200 characters",
  )
  expect(window.localStorage.getItem("hypomnema.bots.layout")).toBe("table")
})

test("the list says so when it cannot be read", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
    json({ code: "internal", message: "boom" }, 500),
  )
  open("/bots")

  expect(await screen.findByText("Couldn't load this list.")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
})

// Scenario: the owner opens a Bot, reads what it keeps, and rewrites its persona. The save sends the model with
// the persona, since the server replaces them together; the memory is not sent and stays.
test("a Bot's persona is saved with its model, and its memory is read only", async () => {
  const { put } = server([
    bot("researcher", {
      provider: "gateway",
      model: "gpt-luna",
      effort: "high",
      persona: "You are the team's researcher.",
      memory: "The owner writes in Vietnamese.\nDeploys go out on Fridays.",
    }),
  ])
  open("/bots/researcher")
  const user = userEvent.setup()

  expect(await screen.findByRole("heading", { name: "researcher", level: 2 })).toBeInTheDocument()
  expect(screen.queryByLabelText("Name")).not.toBeInTheDocument()
  await vi.waitFor(() =>
    expect(screen.getByLabelText("Model")).toHaveTextContent("gpt-lunagateway"),
  )
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()

  // The memory is the Bot's own writing, which the owner may edit; untouched, it offers nothing to save.
  const memory = screen.getByRole("form", { name: "Memory" })
  expect(memory).toHaveTextContent("58 of 2,200 characters")
  expect(within(memory).getByRole("textbox")).toHaveValue(
    "The owner writes in Vietnamese.\nDeploys go out on Fridays.",
  )
  expect(within(memory).queryByRole("button", { name: "Save memory" })).not.toBeInTheDocument()

  const persona = screen.getByLabelText("Persona")
  await user.clear(persona)
  await user.type(persona, "Answer briefly, with sources.")
  await user.click(screen.getByRole("button", { name: "Save" }))

  await vi.waitFor(() =>
    expect(put).toEqual([
      {
        name: "researcher",
        settings: {
          provider: "gateway",
          model: "gpt-luna",
          effort: "high",
          persona: "Answer briefly, with sources.",
          skills: [],
          skills_off: [],
        },
      },
    ]),
  )
  await vi.waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeDisabled())
  expect(screen.getByLabelText("Persona")).toHaveValue("Answer briefly, with sources.")
})

// Scenario: a skill for everyone, one the Bot adds and one it could add (issue #21). The owner turns the first
// off for this Bot, takes the second away and adds the third; they are saved with the Bot.
test("a Bot's skills are turned off, removed and added in its form", async () => {
  const { put } = server([bot("researcher", { skills: ["review-go"] })], {
    skills: [skill("pdf", true), skill("review-go"), skill("forms")],
  })
  open("/bots/researcher")
  const user = userEvent.setup()

  const everyone = await screen.findByRole("list", { name: "Skills for everyone" })
  expect(everyone).toHaveTextContent("What pdf teaches.")
  const pdf = within(everyone).getByRole("switch", { name: /pdf/ })
  expect(pdf).toBeChecked()
  await user.click(pdf)

  const add = screen.getByLabelText("Skills")
  expect(await options(user, add)).toEqual(["forms"])
  await choose(user, add, "forms")
  expect(screen.getByLabelText("Skills")).toBeDisabled()
  await user.click(screen.getByRole("button", { name: "Remove skill review-go" }))
  expect(screen.getByLabelText("Skills")).toBeEnabled()

  await user.click(screen.getByRole("button", { name: "Save" }))
  await vi.waitFor(() => expect(put).toHaveLength(1))
  expect(put[0]?.settings).toMatchObject({ skills: ["forms"], skills_off: ["pdf"] })
})

// Scenario: the owner changes a Bot and thinks again; the form goes back to what the server holds.
test("discarding returns the form to the stored Bot", async () => {
  server([bot("researcher", { persona: "You are the team's researcher." })])
  open("/bots/researcher")
  const user = userEvent.setup()

  await user.type(await screen.findByLabelText("Persona"), " Be brief.")
  await user.click(screen.getByRole("button", { name: "Discard changes" }))

  expect(screen.getByLabelText("Persona")).toHaveValue("You are the team's researcher.")
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
})

// Scenario: the owner removes a Bot that has chats. The server refuses, and the question stays with its reason.
test("a refused removal says why in the dialog", async () => {
  const { removed } = server([bot("researcher")], {
    remove: () => json({ code: "conflict", message: "the bot researcher has chats" }, 409),
  })
  const router = open("/bots/researcher")
  const user = userEvent.setup()

  await user.click(await screen.findByRole("button", { name: "Remove Bot" }))
  const dialog = await screen.findByRole("alertdialog")
  expect(dialog).toHaveTextContent("Remove researcher?")
  await user.click(within(dialog).getByRole("button", { name: "Remove" }))

  expect(await within(dialog).findByRole("alert")).toHaveTextContent("the bot researcher has chats")
  expect(removed).toEqual([])
  expect(router.state.location.pathname).toBe("/bots/researcher")
})

// Scenario: a Bot with no chats and no memory is removed, and the list shows without it.
test("a removed Bot leaves the list", async () => {
  const { removed } = server([bot("coordinator"), bot("researcher")])
  const router = open("/bots/researcher")
  const user = userEvent.setup()

  await user.click(await screen.findByRole("button", { name: "Remove Bot" }))
  await user.click(
    within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Remove" }),
  )

  await vi.waitFor(() => expect(router.state.location.pathname).toBe("/bots"))
  expect(removed).toEqual(["researcher"])
  expect(await screen.findByRole("article", { name: "coordinator" })).toBeInTheDocument()
  expect(screen.queryByRole("article", { name: "researcher" })).not.toBeInTheDocument()
})

const google: Integration = {
  id: "google",
  name: "Google",
  kind: "oauth",
  services: ["gmail"],
  connections: ["work", "home"].map((label) => ({
    integration: "google",
    label,
    account: `${label}@example.com`,
    services: ["gmail"],
    sign_in_required: false,
    chats: label === "work",
    created_at: new Date(0).toISOString(),
    updated_at: new Date(0).toISOString(),
  })),
}

const reviewer = {
  name: "reviewer",
  description: "Reviews a pull request.",
  version_id: "pfv_1",
  version: 1,
  runtime: "claude",
  fallbacks: [],
  skills: [],
  skills_off: [],
  source: "",
  created_at: new Date(0).toISOString(),
} as Profile

// Scenario: the owner limits what a Bot may use (issue #169). Limiting starts with none; the Connections offered
// are those switched on for chats; each switch saves at once with the Bot's stored model and persona, so nothing
// else changes. The workers' card is the same, its profiles each a switch.
test("a Bot's Connections and workers are limited by switches that save at once", async () => {
  const { put } = server(
    [bot("vmap", { provider: "gateway", model: "gpt-luna", persona: "You answer about VMAP." })],
    { integrations: [google], profiles: [reviewer] },
  )
  open("/bots/vmap")
  const user = userEvent.setup()

  const connections = await screen.findByRole("region", { name: "Connections" })
  expect(connections).toHaveTextContent("Every account switched on for chats")

  await user.click(within(connections).getByRole("switch", { name: "Limit Connections" }))
  expect(put.at(-1)).toEqual({
    name: "vmap",
    settings: {
      provider: "gateway",
      model: "gpt-luna",
      persona: "You answer about VMAP.",
      connections: { limited: true, names: [] },
    },
  })

  expect(
    await within(connections).findByText(/No account: every Connection is off/),
  ).toBeInTheDocument()
  expect(within(connections).queryByRole("switch", { name: "Connections: google:home" })).toBeNull()

  await user.click(within(connections).getByRole("switch", { name: "Connections: google:work" }))
  expect(put.at(-1)?.settings.connections).toEqual({ limited: true, names: ["google:work"] })

  const workers = screen.getByRole("region", { name: "Workers" })
  await user.click(within(workers).getByRole("switch", { name: "Limit workers" }))
  expect(put.at(-1)?.settings).toEqual(
    expect.objectContaining({ workers: { limited: true, names: [] } }),
  )
  expect(put.at(-1)?.settings.connections).toBeUndefined()

  await user.click(await within(workers).findByRole("switch", { name: "Workers: reviewer" }))
  expect(put.at(-1)?.settings.workers).toEqual({ limited: true, names: ["reviewer"] })
})

// Each save carries the Bot's whole settings: while the form saves a new persona, the switches wait, so that one
// sending the persona as it was cannot land after it and put it back.
test("a Bot's switches wait while its form saves", async () => {
  let release = () => {}
  const hold = new Promise<void>((resolve) => {
    release = resolve
  })
  server([bot("vmap", { persona: "You answer about VMAP." })], { integrations: [google], hold })
  open("/bots/vmap")
  const user = userEvent.setup()

  const limit = await screen.findByRole("switch", { name: "Limit Connections" })
  await user.clear(screen.getByLabelText("Persona"))
  await user.type(screen.getByLabelText("Persona"), "You answer about maps.")
  await user.click(screen.getByRole("button", { name: "Save" }))

  expect(limit).toHaveAttribute("aria-disabled", "true")
  release()
  await vi.waitFor(() => expect(limit).not.toHaveAttribute("aria-disabled", "true"))
})
