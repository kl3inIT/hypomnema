import { describe, expect, it } from "vitest"

import type { Approval, Bot, Chat, Dispatch, Node } from "@/api/generated/types.gen"
import { board, question, run, task } from "@/features/work/fixtures"
import { boardCards } from "@/features/work/work-state"

import {
  buildTeamMapEdges,
  ownerQuestions,
  teamBots,
  teamLinks,
  teamMachines,
  teamMapSnapshot,
  teamMapStatus,
  type TeamMapSnapshot,
} from "./team-map"

// The Team page's states, links and list, from records as the client API returns them. The edge cases are
// OpenMausBot's (src/lib/team-map.test.ts), ported to Bots handing work to agents.

const now = Date.parse("2026-11-13T12:00:00Z")

const bot = (name: string, m: Partial<Bot> = {}): Bot => ({
  name,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  ...m,
})

const chat = (id: string, botName: string, m: Partial<Chat> = {}): Chat => ({
  id,
  title: `chat ${id}`,
  bot: botName,
  working: false,
  coordinator: true,
  created_at: "2026-11-13T09:00:00Z",
  updated_at: "2026-11-13T09:00:00Z",
  ...m,
})

const dispatch = (node: string, m: Partial<Dispatch> = {}): Dispatch => ({
  id: `dsp_${node}`,
  task_id: "",
  node_id: node,
  status: "running",
  epoch: 1,
  created_at: "2026-11-13T11:58:00Z",
  updated_at: "2026-11-13T11:58:00Z",
  started_at: "2026-11-13T11:58:26Z",
  ...m,
})

const node = (id: string, m: Partial<Node> = {}): Node => ({
  id,
  name: id,
  status: "active",
  max_dispatches: 2,
  runtimes: [],
  connected: true,
  active: 0,
  created_at: "2026-11-01T00:00:00Z",
  ...m,
})

const approval = (chatId: string): Approval => ({
  id: `apr_${chatId}`,
  integration: "google",
  label: "work",
  action: "drive.share",
  action_name: "Share a file",
  asked_by: "pi",
  chat_id: chatId,
  title: "Share a file",
  payload: [],
  status: "pending",
  used: false,
  created_at: "2026-11-13T11:00:00Z",
})

describe("a Bot's status", () => {
  const bots = [bot("coordinator"), bot("key-check"), bot("researcher"), bot("reporter")]

  it("waits for the owner over everything else, then no signal, then working; idle says nothing", () => {
    const chats = [
      chat("c1", "coordinator", { working: true }),
      chat("c2", "key-check", { working: true }),
      chat("c3", "researcher", { coordinator: false }),
      chat("c4", "reporter"),
    ]
    const team = teamBots({ bots, chats, approvals: [approval("c2")], cards: [] })

    expect(team.map((b) => [b.name, teamMapStatus(b).label, b.reason])).toEqual([
      ["coordinator", "Working", "Replying in chat c1"],
      ["key-check", "Waiting for you", "An action waits for your approval"],
      ["researcher", "No signal", "No pi is set up to answer"],
      ["reporter", "", undefined],
    ])
    expect(teamMapStatus({ activity: "waiting-on-you" })).toEqual({
      label: "Waiting for you",
      tone: "warning",
    })
  })

  it("counts the Tasks of its Runs running on machines, and opens the chat they came from", () => {
    const r = run("run_1", { bot: "coordinator", chat_id: "c9" })
    const tasks = [
      task("t1", r.id, { latest_dispatch: dispatch("n1") }),
      task("t2", r.id, { latest_dispatch: dispatch("n2") }),
      task("t3", r.id, { status: "pending" }),
    ]
    const cards = boardCards(board(tasks, [r]))
    const [coordinator] = teamBots({
      bots,
      chats: [chat("c1", "coordinator")],
      approvals: [],
      cards,
    })

    expect(coordinator).toMatchObject({
      activity: "working",
      reason: "2 Tasks running on 2 machines",
      chat: "c9",
    })
  })

  it("waits for the owner while a worker of its Run asks him, found through the chat when the Run names no Bot", () => {
    const r = run("run_1", { chat_id: "c1" })
    const t = task("t1", r.id, {
      latest_dispatch: dispatch("n1", { waiting_since: "2026-11-13T11:56:00Z" }),
    })
    const cards = boardCards(board([t], [r]), [question("q1", t)])
    const [coordinator] = teamBots({
      bots,
      chats: [chat("c1", "coordinator")],
      approvals: [],
      cards,
    })

    expect(coordinator).toMatchObject({
      activity: "waiting-on-you",
      reason: "A worker asks you a question",
      chat: "c1",
    })
  })

  it("opens its newest chat when idle, and none when it has no chat", () => {
    const chats = [
      chat("old", "reporter", { updated_at: "2026-11-12T09:00:00Z" }),
      chat("new", "reporter", { updated_at: "2026-11-13T09:00:00Z" }),
    ]
    const team = teamBots({ bots, chats, approvals: [], cards: [] })

    expect(team.find((b) => b.name === "reporter")?.chat).toBe("new")
    expect(team.find((b) => b.name === "coordinator")?.chat).toBeUndefined()
  })
})

describe("machines and links", () => {
  it("puts each live Dispatch on its machine and links it to the Bot of its Run", () => {
    const r = run("run_1", { bot: "coordinator" })
    const lost = run("run_2")
    const tasks = [
      task("t1", r.id, {
        spec: "Cover /health\nwith a test",
        latest_dispatch: dispatch("n1", { activity: "Bash: go test" }),
      }),
      task("t2", r.id, {
        status: "succeeded",
        latest_dispatch: dispatch("n2", { status: "succeeded" }),
      }),
      task("t3", lost.id, { latest_dispatch: dispatch("n2", { status: "starting" }) }),
    ]
    const cards = boardCards(board(tasks, [r, lost]))
    const machines = teamMachines([node("n1"), node("n2"), node("n3")], cards, [])

    expect(
      machines.map((m) => [m.node.id, m.lines.map((l) => [l.title, l.activity, l.bot])]),
    ).toEqual([
      ["n1", [["Cover /health", "Bash: go test", "coordinator"]]],
      ["n2", [["spec of t3", undefined, undefined]]],
      ["n3", []],
    ])
    // A Run with no Bot draws no link, and a link needs its Bot on the map.
    expect(teamLinks(machines, ["coordinator"])).toEqual([{ bot: "coordinator", node: "n1" }])
    expect(teamLinks(machines, [])).toEqual([])
  })
})

describe("who hands work to whom", () => {
  const handoff = (sourceBot: string, targetAgent: string, m = {}) => ({
    sourceBot,
    targetAgent,
    runtime: "claude",
    objective: "",
    taskId: "t",
    ...m,
  })

  it("keeps one edge per pair and gives live work priority", () => {
    const snapshot: TeamMapSnapshot = {
      collaborations: [handoff("coordinator", "implementer", { lastAt: 10 })],
      queued: [handoff("coordinator", "implementer", { objective: "queued" })],
      running: [handoff("coordinator", "implementer", { objective: "running" })],
    }

    expect(buildTeamMapEdges(["coordinator"], snapshot)).toEqual([
      { ...handoff("coordinator", "implementer", { objective: "running" }), state: "running" },
    ])
  })

  it("filters pairs whose Bot is not on the map, and sorts running, queued, then the newest", () => {
    const snapshot: TeamMapSnapshot = {
      collaborations: [
        handoff("reporter", "explorer", { lastAt: 10 }),
        handoff("reporter", "writer", { lastAt: 20 }),
        handoff("gone", "explorer", { lastAt: 30 }),
      ],
      queued: [handoff("coordinator", "reviewer")],
      running: [handoff("coordinator", "implementer")],
    }

    expect(
      buildTeamMapEdges(["coordinator", "reporter"], snapshot).map((e) => [e.targetAgent, e.state]),
    ).toEqual([
      ["implementer", "running"],
      ["reviewer", "queued"],
      ["writer", "connected"],
      ["explorer", "connected"],
    ])
  })

  it("builds the snapshot from the Tasks: the newest Run's objective, ended work only within a day", () => {
    const older = run("run_1", {
      bot: "coordinator",
      objective: "Old goal",
      created_at: "2026-11-13T08:00:00Z",
    })
    const newer = run("run_2", {
      bot: "coordinator",
      chat_id: "c1",
      objective: "Add a /health page\nwith details",
      created_at: "2026-11-13T10:00:00Z",
    })
    const tasks = [
      task("t1", older.id, { latest_dispatch: dispatch("n1") }),
      task("t2", newer.id, { latest_dispatch: dispatch("n1") }),
      task("t3", newer.id, { profile: "reviewer", status: "ready" }),
      task("t4", older.id, {
        profile: "explorer",
        status: "succeeded",
        finished_at: "2026-11-13T10:00:00Z",
      }),
      task("t5", older.id, {
        profile: "writer",
        status: "succeeded",
        finished_at: "2026-11-11T10:00:00Z",
      }),
    ]
    const cards = boardCards(board(tasks, [older, newer]))
    const edges = buildTeamMapEdges(["coordinator"], teamMapSnapshot(cards, [], now))

    expect(edges.map((e) => [e.targetAgent, e.state, e.objective, e.chatId])).toEqual([
      ["implementer", "running", "Add a /health page", "c1"],
      ["reviewer", "queued", "Add a /health page", "c1"],
      ["explorer", "connected", "Old goal", undefined],
    ])
    expect(edges[0]?.since).toBe("2026-11-13T11:58:26Z")
  })

  it("lists the open questions to the owner, the one waiting longest first", () => {
    const r = run("run_1", { chat_id: "c1" })
    const a = task("t1", r.id)
    const b = task("t2", r.id, { profile: "reviewer" })
    const cards = boardCards(board([a, b], [r]), [
      question("q2", b, { created_at: "2026-11-13T11:00:00Z", text: "Merge it?\nIt is green." }),
      question("q1", a, { created_at: "2026-11-13T10:00:00Z" }),
      question("q3", a, { addressed_to: "coordinator" }),
    ])

    expect(ownerQuestions(cards).map((q) => [q.id, q.agent, q.text, q.chatId])).toEqual([
      ["q1", "implementer", "Public or behind a login?", "c1"],
      ["q2", "reviewer", "Merge it?", "c1"],
    ])
  })
})
