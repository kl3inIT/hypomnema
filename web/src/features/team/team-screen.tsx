import { useNavigate } from "@tanstack/react-router"
import { ArrowRight, ChevronRight, UsersRound } from "lucide-react"
import { useEffect, useMemo, useState, type ReactNode } from "react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { RuntimeMark } from "@/features/agents/runtime-mark"
import { usePending } from "@/features/approvals/use-approvals"
import { useBots } from "@/features/bots/use-bots"
import { useMachines } from "@/features/machines/use-machines"
import { PageHeader, PageHeading } from "@/features/shell/app-shell"
import { useChats } from "@/features/shell/chats"
import { useWork } from "@/features/work/use-work"
import { elapsed, since } from "@/lib/time"

import { TeamCanvas } from "./team-canvas"
import {
  botOf,
  buildTeamMapEdges,
  listRows,
  ownerQuestions,
  teamBots,
  teamLinks,
  teamMachines,
  teamMapSnapshot,
  type OwnerQuestion,
  type TeamBot,
  type TeamMapEdge,
} from "./team-map"

// TeamScreen is the Team page (issue #262), adapted from OpenMausBot's TeamMapPage (Apache License 2.0, Copyright
// 2026 Milind Soni and OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot at 1a4e4da9f,
// src/components/TeamMapPage.tsx; see web/THIRD_PARTY_NOTICES.md): the map, and under it a collapsible "Who hands
// work to whom" of up to twelve rows (EdgeRow), a row opening the work's thread; the chats read every 3 s while the
// page is visible. Changed: no /team-map endpoint, as every link here is a record the server already serves (the
// Bots, chats, pending Approvals, Tasks with their Runs and questions, machines), kept live by the event stream;
// the questions to the owner come first in the list; a row says its state in words with the duration or the time;
// no composer, departments, Add menu, "Show computers" switch or computers' panel.
export function TeamScreen({
  task,
  onOpenTask,
}: {
  task?: string
  onOpenTask: (taskId: string) => void
}) {
  const navigate = useNavigate()
  const bots = useBots()
  const chats = useChats({ every: 3000 })
  const pending = usePending()
  const machines = useMachines()
  const { cards } = useWork()
  const now = useNow()

  const list = useMemo(() => bots.data ?? [], [bots.data])
  const chatList = useMemo(() => chats.data ?? [], [chats.data])
  const team = useMemo(
    () => teamBots({ bots: list, chats: chatList, approvals: pending.data ?? [], cards }),
    [list, chatList, pending.data, cards],
  )
  const work = useMemo(
    () => teamMachines(machines.data ?? [], cards, chatList),
    [machines.data, cards, chatList],
  )
  const names = useMemo(() => list.map((b) => b.name), [list])
  const links = useMemo(() => teamLinks(work, names), [work, names])
  const questions = useMemo(() => ownerQuestions(cards), [cards])
  const edges = useMemo(
    () => buildTeamMapEdges(names, teamMapSnapshot(cards, chatList, now)),
    [names, cards, chatList, now],
  )
  const selected = useMemo(() => {
    if (!task) return undefined

    const card = cards.find((c) => c.task.id === task)
    const byId = new Map(chatList.map((c) => [c.id, c]))

    return {
      task,
      bot: card ? botOf(card, byId) : undefined,
      node: card?.task.latest_dispatch?.node_id,
    }
  }, [task, cards, chatList])

  // A Bot opens its chat that waits, else the one that works, else its newest, else a new chat with it
  // (OpenMausBot's openBot).
  const openBot = (bot: TeamBot) =>
    bot.chat
      ? void navigate({ to: "/chats/$chatId", params: { chatId: bot.chat } })
      : void navigate({ to: "/", search: { bot: bot.name } })
  // A row opens the chat its Run came from, where the work is; a Run with no chat opens the Task.
  const openWork = (chatId: string | undefined, taskId: string) =>
    chatId ? void navigate({ to: "/chats/$chatId", params: { chatId } }) : onOpenTask(taskId)

  return (
    <main className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto">
      <PageHeader />
      <div className="shrink-0 px-3 pt-1 sm:px-5">
        <PageHeading
          title="Team"
          description="Your Bots and the machines their work runs on. A Bot opens its chat; a Task on a machine opens its details."
        />
      </div>
      {bots.error ? (
        <div className="flex flex-col items-center gap-3 py-10 text-sm text-muted-foreground">
          <p>Couldn&apos;t load the team.</p>
          <Button variant="outline" size="sm" onClick={() => void bots.refetch()}>
            Retry
          </Button>
        </div>
      ) : bots.isPending ? (
        <div className="m-3 flex-1 animate-pulse rounded-xl bg-muted sm:m-5" aria-label="Loading" />
      ) : list.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <UsersRound />
            </EmptyMedia>
            <EmptyTitle>No Bots yet</EmptyTitle>
            <EmptyDescription>
              A Bot talks with you in chats and hands work to workers on your machines. Make one on
              the Bots page.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <>
          <div className="flex min-h-88 flex-1 flex-col">
            <TeamCanvas
              bots={team}
              machines={work}
              links={links}
              selected={selected}
              now={now}
              onOpenBot={openBot}
              onOpenTask={onOpenTask}
            />
          </div>
          <Handoffs questions={questions} edges={edges} now={now} onOpen={openWork} />
        </>
      )}
    </main>
  )
}

// useNow ticks every second, for the durations the page shows.
function useNow() {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)

    return () => window.clearInterval(timer)
  }, [])

  return now
}

// Handoffs is OpenMausBot's "Who hands work to whom": the questions to the owner, then a row a Bot and agent pair.
function Handoffs({
  questions,
  edges,
  now,
  onOpen,
}: {
  questions: OwnerQuestion[]
  edges: TeamMapEdge[]
  now: number
  onOpen: (chatId: string | undefined, taskId: string) => void
}) {
  const count = questions.length + edges.length
  if (count === 0) return null

  const shownQuestions = questions.slice(0, listRows)
  const shownEdges = edges.slice(0, listRows - shownQuestions.length)

  return (
    // OpenMausBot's own <details>, closed at first so the map has the height: it opens and closes with no state
    // to keep.
    <details className="group shrink-0 border-t px-3 py-2.5 sm:px-5">
      <summary className="flex w-fit cursor-pointer list-none items-center gap-1.5 rounded-md py-1 text-sm font-medium text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight
          className="size-4 text-muted-foreground transition-transform group-open:rotate-90"
          aria-hidden
        />
        Who hands work to whom · {count}
      </summary>
      <ul aria-label="Who hands work to whom" className="mt-1.5 max-h-56 overflow-y-auto">
        {shownQuestions.map((q) => (
          <Row
            key={q.id}
            state={<Badge variant="warning">Needs you</Badge>}
            onOpen={() => onOpen(q.chatId, q.taskId)}
            trailing={
              <span className="inline-flex items-center gap-1">
                Answer
                <ArrowRight className="size-3.5" aria-hidden />
              </span>
            }
          >
            <RuntimeMark runtime={q.runtime} className="size-4.5" />
            <span className="shrink-0 font-medium">{q.agent} asks:</span>
            <span className="truncate text-muted-foreground">“{q.text}”</span>
          </Row>
        ))}
        {shownEdges.map((edge) => (
          <Row
            key={`${edge.sourceBot}:${edge.targetAgent}`}
            state={
              edge.state === "running" ? (
                <Badge variant="running">Running</Badge>
              ) : edge.state === "queued" ? (
                <Badge variant="outline">Queued</Badge>
              ) : (
                <Badge variant="secondary">Done</Badge>
              )
            }
            onOpen={() => onOpen(edge.chatId, edge.taskId)}
            trailing={
              edge.state === "running"
                ? edge.since
                  ? elapsed(edge.since, now)
                  : "–"
                : edge.state === "connected" && edge.lastAt
                  ? since(new Date(edge.lastAt).toISOString(), now)
                  : "–"
            }
          >
            <span className="shrink-0 font-medium">{edge.sourceBot}</span>
            <ArrowRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
            <RuntimeMark runtime={edge.runtime} className="size-4.5" />
            <span className="shrink-0 font-medium">{edge.targetAgent}</span>
            <span className="truncate text-muted-foreground">· {edge.objective}</span>
          </Row>
        ))}
      </ul>
    </details>
  )
}

// Row is a line of the list that opens what it is about; it is its own design, as a row that opens
// (docs/conventions.md, web client).
function Row({
  state,
  trailing,
  onOpen,
  children,
}: {
  state: ReactNode
  trailing: ReactNode
  onOpen: () => void
  children: ReactNode
}) {
  return (
    <li className="border-t border-border/60 first:border-t-0">
      <button
        type="button"
        data-testid="team-handoff"
        onClick={onOpen}
        className="grid w-full grid-cols-[6.5rem_minmax(0,1fr)_auto] items-center gap-3 rounded-md px-1 py-2 text-left text-sm outline-none hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring max-sm:grid-cols-[5.5rem_minmax(0,1fr)]"
      >
        <span>{state}</span>
        <span className="flex min-w-0 items-center gap-1.5">{children}</span>
        <span className="text-xs whitespace-nowrap text-muted-foreground tabular-nums max-sm:col-start-2">
          {trailing}
        </span>
      </button>
    </li>
  )
}
