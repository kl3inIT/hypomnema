import type { Approval, Bot, Chat, Dispatch, Node } from "@/api/generated/types.gen"
import type { Card } from "@/features/work/work-state"
import { firstLine } from "@/features/work/work-state"

// What the Team page draws, worked out from the records the control plane already serves (issue #262).
//
// Adapted from OpenMausBot (Apache License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors,
// https://github.com/milind-soni/OpenMausBot at 1a4e4da9f; see web/THIRD_PARTY_NOTICES.md), src/lib/team-map.ts:
// teamMapStatus's order (Waiting for you > No signal > Working > idle) and buildTeamMapEdges (one row per pair,
// live work first, then the newest). Changed: a Bot's activity is computed here from its chats, the pending
// Approvals and the Runs' Tasks, where OpenMausBot's server stores it; an edge goes from a Bot to the agent a Task
// of its Run runs as, keyed in that order, as Bots and agents are different things here; departments
// (buildTeamMapSections) and the bot count are left out; the words are English only, with no i18n.

export type BotActivity = "waiting-on-you" | "no-signal" | "working" | "idle"

export type TeamBot = {
  name: string
  model?: string
  provider?: string
  activity: BotActivity
  // One plain line saying why the status is what it is; nothing when the Bot is idle.
  reason?: string
  // The chat a click opens: the one that waits, else the one that works, else the newest; none starts a new one.
  chat?: string
}

export type TeamMapStatus = {
  label: string
  tone: "warning" | "danger" | "running" | "idle"
}

// teamMapStatus is OpenMausBot's: waiting on the owner beats no signal, which beats working. Idle shows no label.
export function teamMapStatus(bot: Pick<TeamBot, "activity">): TeamMapStatus {
  if (bot.activity === "waiting-on-you") return { label: "Waiting for you", tone: "warning" }
  if (bot.activity === "no-signal") return { label: "No signal", tone: "danger" }
  if (bot.activity === "working") return { label: "Working", tone: "running" }

  return { label: "", tone: "idle" }
}

// A Dispatch whose worker is on a machine now: starting, running or stopping.
const liveStatuses = new Set<Dispatch["status"]>(["starting", "running", "stopping"])

export function liveDispatch(card: Card): Dispatch | undefined {
  const dispatch = card.task.latest_dispatch

  return dispatch && liveStatuses.has(dispatch.status) ? dispatch : undefined
}

// botOf is the Bot whose chat a Run came from: Run.bot, or the chat's Bot for a server that does not say it.
export function botOf(card: Card, chats: ReadonlyMap<string, Chat>): string | undefined {
  return card.run.bot || (card.run.chat_id ? chats.get(card.run.chat_id)?.bot : undefined)
}

const plural = (n: number, one: string, many: string) => (n === 1 ? `${n} ${one}` : `${n} ${many}`)

const newest = (a: { updated_at: string }, b: { updated_at: string }) =>
  b.updated_at.localeCompare(a.updated_at)

// teamBots gives each Bot its status, the line that explains it and the chat it opens.
export function teamBots({
  bots,
  chats,
  approvals,
  cards,
}: {
  bots: Bot[]
  chats: Chat[]
  approvals: Approval[]
  cards: Card[]
}): TeamBot[] {
  const byId = new Map(chats.map((c) => [c.id, c]))

  return bots.map((bot) => {
    const own = chats.filter((c) => c.bot === bot.name).sort(newest)
    const ids = new Set(own.map((c) => c.id))
    const asking = approvals.filter((a) => a.chat_id && ids.has(a.chat_id))
    const work = cards.filter((c) => botOf(c, byId) === bot.name)
    const needs = work.filter((c) => c.column === "you")
    const questions = needs.filter((c) => c.question)
    const live = work.filter((c) => liveDispatch(c))
    const working = own.filter((c) => c.working)
    const base = { name: bot.name, model: bot.model, provider: bot.provider }
    const newestChat = own[0]?.id

    if (asking.length > 0 || needs.length > 0) {
      return {
        ...base,
        activity: "waiting-on-you",
        reason:
          asking.length > 0
            ? asking.length === 1
              ? "An action waits for your approval"
              : `${asking.length} actions wait for your approval`
            : questions.length > 0
              ? questions.length === 1
                ? "A worker asks you a question"
                : `${questions.length} questions wait for you`
              : `${plural(needs.length, "Task needs", "Tasks need")} you`,
        chat: asking[0]?.chat_id ?? needs.find((c) => c.run.chat_id)?.run.chat_id ?? newestChat,
      }
    }

    // pi's coordinator is set up for every chat or none; the newest chat says which.
    if (own[0] && !own[0].coordinator) {
      return {
        ...base,
        activity: "no-signal",
        reason: "No pi is set up to answer",
        chat: newestChat,
      }
    }

    if (live.length > 0 || working.length > 0) {
      const machines = new Set(live.map((c) => liveDispatch(c)?.node_id)).size

      return {
        ...base,
        activity: "working",
        reason:
          live.length > 0
            ? `${plural(live.length, "Task", "Tasks")} running on ${plural(machines, "machine", "machines")}`
            : `Replying in ${working[0]?.title}`,
        chat: working[0]?.id ?? live.find((c) => c.run.chat_id)?.run.chat_id ?? newestChat,
      }
    }

    return { ...base, activity: "idle", chat: newestChat }
  })
}

// A line on a machine's card: one live Dispatch, with what its worker does now.
export type MachineLine = {
  taskId: string
  runtime: string
  title: string
  status: Dispatch["status"]
  activity?: string
  waitingSince?: string
  bot?: string
}

export type TeamMachine = { node: Node; lines: MachineLine[] }

// teamMachines puts each live Dispatch on the machine it runs on, the earliest first.
export function teamMachines(nodes: Node[], cards: Card[], chats: Chat[]): TeamMachine[] {
  const byId = new Map(chats.map((c) => [c.id, c]))
  const lines = new Map<string, MachineLine[]>()
  const live = cards
    .filter((c) => liveDispatch(c))
    .sort((a, b) => a.task.created_at.localeCompare(b.task.created_at))

  for (const card of live) {
    const dispatch = card.task.latest_dispatch as Dispatch
    const list = lines.get(dispatch.node_id) ?? []
    list.push({
      taskId: card.task.id,
      runtime: card.runtime,
      title: firstLine(card.task.spec),
      status: dispatch.status,
      activity: dispatch.activity,
      waitingSince: dispatch.waiting_since,
      bot: botOf(card, byId),
    })
    lines.set(dispatch.node_id, list)
  }

  return nodes.map((node) => ({ node, lines: lines.get(node.id) ?? [] }))
}

export type TeamLink = { bot: string; node: string }

// teamLinks joins a Bot to a machine while a Dispatch of one of its Runs is live there (OpenMausBot draws a bot's
// link to its computer while the bot is busy, TeamCanvas.tsx). A Run with no Bot draws none.
export function teamLinks(machines: TeamMachine[], bots: string[]): TeamLink[] {
  const known = new Set(bots)
  const links = new Map<string, TeamLink>()

  for (const { node, lines } of machines) {
    for (const line of lines) {
      if (line.bot && known.has(line.bot)) {
        links.set(`${line.bot}\n${node.id}`, { bot: line.bot, node: node.id })
      }
    }
  }

  return [...links.values()]
}

// A handoff as OpenMausBot's snapshot carries it: from a Bot to the agent a Task runs as.
export type Handoff = {
  sourceBot: string
  targetAgent: string
  runtime: string
  objective: string
  chatId?: string
  taskId: string
  // When the worker started, for a running handoff.
  since?: string
  lastAt?: number
}

export type TeamMapSnapshot = {
  collaborations: Handoff[]
  queued: Handoff[]
  running: Handoff[]
}

export type TeamMapEdge = Handoff & { state: "running" | "queued" | "connected" }

const day = 24 * 60 * 60 * 1000

// teamMapSnapshot sorts the Tasks of Runs that came from a Bot into OpenMausBot's three lists: running, queued,
// and those that ended within the last 24 hours. Each list runs from the oldest Run to the newest, so the newest
// Run's row is the one kept for a pair.
export function teamMapSnapshot(cards: Card[], chats: Chat[], now = Date.now()): TeamMapSnapshot {
  const byId = new Map(chats.map((c) => [c.id, c]))
  const snapshot: TeamMapSnapshot = { collaborations: [], queued: [], running: [] }
  const ordered = [...cards].sort(
    (a, b) =>
      a.run.created_at.localeCompare(b.run.created_at) ||
      a.task.created_at.localeCompare(b.task.created_at),
  )

  for (const card of ordered) {
    const sourceBot = botOf(card, byId)
    if (!sourceBot) continue

    const dispatch = liveDispatch(card)
    const handoff: Handoff = {
      sourceBot,
      targetAgent: card.task.profile,
      runtime: card.runtime,
      objective: firstLine(card.run.objective),
      chatId: card.run.chat_id,
      taskId: card.task.id,
    }

    if (dispatch || card.task.status === "running") {
      const started = dispatch?.started_at ?? dispatch?.created_at
      snapshot.running.push({ ...handoff, since: started })
    } else if (card.queued) {
      snapshot.queued.push(handoff)
    } else {
      const lastAt = Date.parse(card.task.finished_at ?? card.task.updated_at)
      if (now - lastAt <= day) snapshot.collaborations.push({ ...handoff, lastAt })
    }
  }

  return snapshot
}

// buildTeamMapEdges is OpenMausBot's: one edge a pair, a running handoff over a queued one over one that ended,
// then the newest first. Only a pair whose Bot is on the map is kept.
export function buildTeamMapEdges(bots: string[], snapshot: TeamMapSnapshot): TeamMapEdge[] {
  const visible = new Set(bots)
  const edges = new Map<string, TeamMapEdge>()
  const key = (h: Handoff) => `${h.sourceBot}\n${h.targetAgent}`

  for (const h of snapshot.collaborations) {
    if (visible.has(h.sourceBot)) edges.set(key(h), { ...h, state: "connected" })
  }
  for (const h of snapshot.queued) {
    if (visible.has(h.sourceBot)) edges.set(key(h), { ...h, state: "queued" })
  }
  for (const h of snapshot.running) {
    if (visible.has(h.sourceBot)) edges.set(key(h), { ...h, state: "running" })
  }

  const priority = { running: 0, queued: 1, connected: 2 } as const

  return [...edges.values()].sort(
    (a, b) => priority[a.state] - priority[b.state] || (b.lastAt ?? 0) - (a.lastAt ?? 0),
  )
}

// An open question from a worker to the owner, as the list's first rows show it.
export type OwnerQuestion = {
  id: string
  agent: string
  runtime: string
  text: string
  chatId?: string
  taskId: string
}

// ownerQuestions lists what workers asked the owner, the one waiting longest first.
export function ownerQuestions(cards: Card[]): OwnerQuestion[] {
  return cards
    .filter((c) => c.question)
    .sort((a, b) => (a.question?.created_at ?? "").localeCompare(b.question?.created_at ?? ""))
    .map((c) => ({
      id: c.question?.id ?? "",
      agent: c.task.profile,
      runtime: c.runtime,
      text: firstLine(c.question?.text ?? ""),
      chatId: c.run.chat_id,
      taskId: c.task.id,
    }))
}

// The list shows at most this many rows, as OpenMausBot's.
export const listRows = 12
