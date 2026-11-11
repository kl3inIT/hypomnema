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
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { Model, Profile } from "@/api/generated/types.gen"

import { look } from "./agent-look"
import { AgentsScreen } from "./agents-screen"
import { NewAgentScreen, ProfileScreen } from "./profile-screen"

// Monaco needs a real browser; here the YAML is a plain field with the same name, value and change. The editor
// itself is covered by the browser tests (tests/agents.spec.ts).
vi.mock("./yaml-editor", () => ({
  default: ({
    label,
    value,
    onChange,
  }: {
    label: string
    value: string
    onChange: (value: string) => void
  }) => (
    <textarea aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} />
  ),
}))
import { toYaml, type Draft } from "./profiles-state"

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

const profile = (name: string, m: Partial<Profile> = {}): Profile => ({
  name,
  description: `What ${name} is for.`,
  version_id: `pfv_${name}_1`,
  version: 1,
  runtime: "claude",
  fallbacks: [],
  source: `name: ${name}\ndescription: What ${name} is for.\nruntime: claude\n`,
  created_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
  available: true,
  ...m,
})

const models: Model[] = [
  { provider: "claude-subscription", name: "opus", kind: "claude_subscription" },
  { provider: "gateway", name: "gpt-sol", kind: "model_gateway" },
]

// The server: the profiles, their versions and the models, and one answer per PUT; it keeps what was applied.
function server(
  profiles: Profile[],
  opts: {
    versions?: Record<string, Profile[]>
    apply?: (name: string, source: string) => Response
  } = {},
) {
  const applied: { name: string; source: string }[] = []

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")

    if (request.method === "GET" && path === "/profiles") return json({ data: profiles })
    if (request.method === "GET" && path === "/models") return json({ data: models })

    const versions = /^\/profiles\/([^/]+)\/versions$/.exec(path)
    if (request.method === "GET" && versions) {
      return json({ data: opts.versions?.[versions[1]!] ?? [] })
    }

    const one = /^\/profiles\/([^/]+)$/.exec(path)
    if (request.method === "GET" && one) {
      const found = profiles.find((p) => p.name === one[1])

      return found ? json(found) : json({ code: "not_found", message: "no such profile" }, 404)
    }

    if (request.method === "PUT" && one) {
      const { source } = (await request.clone().json()) as { source: string }
      applied.push({ name: one[1]!, source })

      return opts.apply?.(one[1]!, source) ?? json(profile(one[1]!, { version: 2, source }), 201)
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return applied
}

function open(path: string) {
  const root = createRootRoute({ component: Outlet })
  const routes = [
    createRoute({ getParentRoute: () => root, path: "/agents", component: AgentsScreen }),
    createRoute({ getParentRoute: () => root, path: "/agents/new", component: NewAgentScreen }),
    createRoute({
      getParentRoute: () => root,
      path: "/agents/$name",
      component: function One() {
        const { name } = routes[2]!.useParams() as { name: string }

        return <ProfileScreen key={name} name={name} />
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

const agent = (name: string) => screen.findByRole("article", { name })

// Scenario: three workers' profiles and the coordinator's. Each card says its model and whether a worker can
// start now, in words; the coordinator's says nothing about that; the header counts.
test("a card says what the agent runs on and whether it can run now", async () => {
  server([
    profile("implementer", { model: "opus", provider: "claude-subscription", effort: "xhigh" }),
    profile("codex-implementer", {
      runtime: "codex",
      available: false,
      unavailable_reason: "no_node",
    }),
    profile("reviewer", { available: false, unavailable_reason: "no_account" }),
    profile("coordinator", { runtime: "pi", available: undefined }),
  ])
  open("/agents")

  const implementer = await agent("implementer")
  expect(implementer).toHaveTextContent("What implementer is for.")
  expect(implementer).toHaveTextContent("Model:opus(claude-subscription)")
  expect(implementer).toHaveTextContent("Effort:Extra High")
  expect(implementer).toHaveTextContent("Version:1saved 2h ago")
  expect(implementer).toHaveTextContent("Can run now")

  expect(await agent("codex-implementer")).toHaveTextContent("No machine online has Codex")
  expect(await agent("reviewer")).toHaveTextContent("No Claude Code account")
  expect(await agent("coordinator")).not.toHaveTextContent(/can run|cannot|No /i)
  expect(await agent("coordinator")).toHaveTextContent("Runtime default")
  expect(screen.getByText("1 can run · 2 cannot")).toBeInTheDocument()
})

// Scenario: the owner prefers the table; the choice is kept in this browser, and the table says the same things.
test("the table shows the same agents and the layout is remembered", async () => {
  server([profile("implementer"), profile("explorer", { effort: "low" })])
  open("/agents")
  await agent("implementer")

  await userEvent.click(screen.getByRole("button", { name: "table" }))

  const rows = screen.getAllByTestId("agent")
  expect(rows).toHaveLength(2)
  expect(rows[1]).toHaveTextContent("explorer")
  expect(rows[1]).toHaveTextContent("Low")
  expect(rows[1]).toHaveTextContent("Can run now")
  expect(window.localStorage.getItem("hypomnema.agents.layout")).toBe("table")
})

test("a list that cannot be read offers to try again, and an empty one says what an agent is", async () => {
  const fetch = vi
    .spyOn(globalThis, "fetch")
    .mockImplementation(async () => json({ code: "internal", message: "boom" }, 500))
  open("/agents")

  expect(await screen.findByText("Couldn't load this list.")).toBeInTheDocument()

  fetch.mockImplementation(async () => json({ data: [] }))
  await userEvent.click(screen.getByRole("button", { name: "Retry" }))

  expect(await screen.findByText(/No agents yet\. An agent is a saved profile/)).toBeInTheDocument()
})

// Scenario: the owner makes a Codex agent from the builder. What is sent is the YAML the server stores, under
// the name typed, and the page opens the new agent.
test("a new agent is sent as YAML built from the fields", async () => {
  const applied = server([profile("codex-implementer", { runtime: "codex", version: 2 })])
  const router = open("/agents/new")
  const user = userEvent.setup()

  const create = await screen.findByRole("button", { name: "Create profile" })
  expect(create).toBeDisabled()

  await user.type(screen.getByLabelText("Name"), "codex-implementer")
  await user.type(screen.getByLabelText("Description"), "Makes a narrow change.")
  await user.selectOptions(screen.getByLabelText("Runs on"), "codex")
  await user.selectOptions(await screen.findByLabelText("Model"), "gateway/gpt-sol")
  await user.click(
    within(screen.getByRole("group", { name: "Effort" })).getByRole("button", { name: "High" }),
  )
  await user.type(screen.getByLabelText("Instructions"), "Keep the change small.")
  await user.click(create)

  await screen.findByRole("heading", { name: "codex-implementer" })
  expect(router.state.location.pathname).toBe("/agents/codex-implementer")
  expect(applied).toEqual([
    {
      name: "codex-implementer",
      source:
        "name: codex-implementer\n" +
        'description: "Makes a narrow change."\n' +
        "runtime: codex\n" +
        "model:\n  provider: gateway\n  name: gpt-sol\n  effort: high\n" +
        'instruction: "Keep the change small."\n',
    },
  ])
})

test("a name with other characters is not sent, and the form says why", async () => {
  const applied = server([])
  open("/agents/new")
  const user = userEvent.setup()

  await user.type(await screen.findByLabelText("Name"), "my agent")
  await user.type(screen.getByLabelText("Description"), "x")

  expect(
    screen.getByText("Use only letters, numbers, hyphens, and underscores."),
  ).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Create profile" })).toBeDisabled()
  expect(applied).toEqual([])
})

// Scenario: the owner changes an agent's description. The name cannot change; Save revision is off until
// something changed; a refusal of the server is shown in its words and the edit stays.
test("saving an agent makes a revision, and a refusal is shown in the server's words", async () => {
  const applied = server([profile("reviewer")], {
    apply: () =>
      json({ code: "invalid", message: 'model "opus" cannot run against provider gateway' }, 422),
  })
  open("/agents/reviewer")
  const user = userEvent.setup()

  const save = await screen.findByRole("button", { name: "Save revision" })
  expect(save).toBeDisabled()
  expect(screen.getByLabelText("Name")).toHaveAttribute("readonly")
  expect(screen.getByText("The name cannot be changed.")).toBeInTheDocument()

  const description = screen.getByLabelText("Description")
  await user.clear(description)
  await user.type(description, "Reviews a change.")
  await user.click(save)

  expect(
    await screen.findByText('model "opus" cannot run against provider gateway'),
  ).toBeInTheDocument()
  expect(applied[0]?.source).toContain('description: "Reviews a change."')
  expect(screen.getByLabelText("Description")).toHaveValue("Reviews a change.")

  await user.click(screen.getByRole("button", { name: "Discard changes" }))
  expect(screen.getByLabelText("Description")).toHaveValue("What reviewer is for.")
  expect(screen.getByRole("button", { name: "Save revision" })).toBeDisabled()
})

// Scenario: the owner orders the agents that take this one's Tasks while it is out of quota (issue #171). The
// other workers are offered, never the agent itself or the coordinator; the order chosen is the order saved, and a
// row can be removed.
test("fallbacks are chosen in order from the other workers and saved with the agent", async () => {
  const applied = server([
    profile("writer"),
    profile("codex-worker", { runtime: "codex" }),
    profile("omp-worker", { runtime: "omp" }),
    profile("coordinator", { runtime: "pi", available: undefined }),
  ])
  open("/agents/writer")
  const user = userEvent.setup()

  const add = await screen.findByLabelText("Fallbacks")
  await vi.waitFor(() =>
    expect(
      within(add)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Add a fallback…", "codex-worker", "omp-worker"]),
  )

  await user.selectOptions(add, "omp-worker")
  await user.selectOptions(screen.getByLabelText("Fallbacks"), "codex-worker")

  const rows = within(screen.getByRole("list", { name: "Fallbacks, in order" })).getAllByRole(
    "listitem",
  )
  expect(rows).toHaveLength(2)
  expect(within(rows[0]!).getByLabelText("Fallback 1")).toHaveValue("omp-worker")
  expect(screen.getByLabelText("Fallbacks")).toBeDisabled()

  await user.click(screen.getByRole("button", { name: "Save revision" }))
  await vi.waitFor(() => expect(applied).toHaveLength(1))
  expect(applied[0]?.source).toContain("fallbacks: [omp-worker, codex-worker]\n")

  await user.click(screen.getByRole("button", { name: "Remove fallback omp-worker" }))
  expect(screen.getByLabelText("Fallback 1")).toHaveValue("codex-worker")
})

// Scenario: the owner looks for an agent by a word of its name or of its description; a word nothing has says so,
// which is not the empty list's invitation to make the first agent.
test("the search narrows the agents by name and description", async () => {
  server([
    profile("implementer"),
    profile("reviewer", { description: "Reads a pull request against its spec." }),
  ])
  open("/agents")
  const user = userEvent.setup()

  await agent("implementer")
  const search = screen.getByRole("searchbox", { name: "Search agents" })

  await user.type(search, "pull request")
  expect(screen.getAllByTestId("agent").map((a) => a.getAttribute("aria-label"))).toEqual([
    "reviewer",
  ])

  await user.clear(search)
  await user.type(search, "IMPLEM")
  expect(screen.getAllByTestId("agent").map((a) => a.getAttribute("aria-label"))).toEqual([
    "implementer",
  ])

  await user.clear(search)
  await user.type(search, "nothing has this")
  expect(screen.getByText("No results.")).toBeInTheDocument()
  expect(screen.queryByText(/No agents yet/)).toBeNull()
})

// Scenario: a starting profile was put in by a migration and has no time of its own; the card gives its version
// and no age, where it used to count the days since 1970.
test("a version with no time shows no age", async () => {
  server([profile("implementer", { created_at: new Date(0).toISOString() })])
  open("/agents")

  const card = await agent("implementer")
  expect(card).toHaveTextContent("Version:1")
  expect(card).not.toHaveTextContent(/saved|ago/)
})

// An agent keeps its face: the same name gives the same body and colour, and the starting agents differ.
test("an agent's avatar comes from its name", () => {
  expect(look("reviewer")).toEqual(look("reviewer"))

  const faces = ["implementer", "reviewer", "explorer", "coordinator"].map((name) => {
    const { body, color } = look(name)

    return `${body.id} ${color}`
  })
  expect(new Set(faces).size).toBe(4)
})

// Scenario: the YAML is edited by hand, then the owner goes back to the builder. The builder cannot read YAML,
// so it asks first; keeping the edits stays on YAML, discarding them returns to the fields as they were.
test("going back to the builder asks before YAML edits are discarded", async () => {
  const applied = server([profile("reviewer")])
  open("/agents/reviewer")
  const user = userEvent.setup()

  await user.click(await screen.findByRole("button", { name: "YAML" }))
  const yaml = await screen.findByRole("textbox", { name: "Config (YAML)" })
  expect(yaml).toHaveValue(profile("reviewer").source)

  await user.type(yaml, "instruction: by hand\n")
  await user.click(screen.getByRole("button", { name: "Builder" }))

  const dialog = await screen.findByRole("alertdialog", { name: "Discard YAML edits?" })
  await user.click(within(dialog).getByRole("button", { name: "Keep editing YAML" }))
  expect(screen.getByRole("textbox", { name: "Config (YAML)" })).toHaveValue(
    `${profile("reviewer").source}instruction: by hand\n`,
  )

  await user.click(screen.getByRole("button", { name: "Save revision" }))
  expect(applied[0]?.source).toBe(`${profile("reviewer").source}instruction: by hand\n`)
})

// Scenario: an earlier version is restored. Its source is applied again as a new version; nothing is removed.
test("restoring a version applies its source again", async () => {
  const first = profile("reviewer", {
    version: 1,
    version_id: "pfv_1",
    source: "name: reviewer\n# one\n",
  })
  const second = profile("reviewer", {
    version: 2,
    version_id: "pfv_2",
    source: "name: reviewer\n# two\n",
  })
  const applied = server([second], { versions: { reviewer: [second, first] } })
  open("/agents/reviewer")
  const user = userEvent.setup()

  await user.click(await screen.findByRole("button", { name: "versions" }))
  const list = await screen.findByRole("list", { name: "Versions" })
  const rows = within(list).getAllByRole("listitem")
  expect(rows[0]).toHaveTextContent("Version 2")
  expect(rows[0]).toHaveTextContent("Current")
  expect(within(rows[0]!).queryByRole("button", { name: "Restore this version" })).toBeNull()

  await user.click(within(rows[1]!).getByText("Version 1"))
  await user.click(within(rows[1]!).getByRole("button", { name: "Restore this version" }))
  const dialog = await screen.findByRole("alertdialog", { name: "Restore version 1?" })
  await user.click(within(dialog).getByRole("button", { name: "Restore" }))

  await vi.waitFor(() => expect(applied).toEqual([{ name: "reviewer", source: first.source }]))
})

const draft = (m: Partial<Draft>): Draft => ({
  name: "a",
  description: "d",
  runtime: "claude",
  model: "",
  effort: "",
  instruction: "",
  fallbacks: [],
  ...m,
})

// The YAML the builder writes is read by the server's strict decoder, so what YAML would read as something else
// (a number, a boolean, a colon) is quoted, and several lines keep their breaks.
test("the builder's YAML quotes what YAML would misread and keeps lines", () => {
  expect(toYaml(draft({ name: "123", description: "yes: no # not a comment" }), models)).toBe(
    'name: "123"\ndescription: "yes: no # not a comment"\nruntime: claude\n',
  )
  expect(toYaml(draft({ effort: "low" }), models)).toBe(
    'name: a\ndescription: "d"\nruntime: claude\nmodel:\n  effort: low\n',
  )
  expect(toYaml(draft({ model: "gpt-5/turbo" }), models)).toContain(
    'model:\n  name: "gpt-5/turbo"\n',
  )
  expect(toYaml(draft({ instruction: "Read only.\n\nReport with file paths.\n" }), models)).toBe(
    'name: a\ndescription: "d"\nruntime: claude\ninstruction: |\n  Read only.\n\n  Report with file paths.\n',
  )
  expect(toYaml(draft({ fallbacks: ["codex-worker", "omp-worker"] }), models)).toBe(
    'name: a\ndescription: "d"\nruntime: claude\nfallbacks: [codex-worker, omp-worker]\n',
  )
  expect(toYaml(draft({ instruction: "  indented first line\nsecond" }), models)).toContain(
    'instruction: "  indented first line\\nsecond"\n',
  )
})
