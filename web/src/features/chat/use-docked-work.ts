import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import "@/api/client"
import {
  getRunOptions,
  getRunQueryKey,
  listQuestionsOptions,
  listQuestionsQueryKey,
  listRunsOptions,
  listRunsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import type { Question, RunDetail } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

import type { Dock } from "./dock-context"
import { docked, startOpen, toRead } from "./dock-state"

// The chat's own work docked above its composer (issue #314), read for docked-work.tsx: its open Runs with their
// Tasks, the Runs a wake-up's chip asked to see, and the open questions to the owner from those Runs.

// The Runs are read every 10 s, while a worker runs; the questions every 3 s, as the Approvals
// (use-approvals.ts).
const runsEvery = 10_000
const questionsEvery = 3_000

// dockId is the id of a Run's strip, which a wake-up's chip scrolls into view.
export const dockId = (chatId: string, runId: string) => `dock-${chatId}-${runId}`

export type DockedWork = {
  runs: RunDetail[]
  questions: Question[]
  open: Record<string, boolean>
  setOpen: (runId: string, open: boolean) => void
  dock: Dock
}

// useDockedWork reads the chat's open Runs with their Tasks and the open questions to the owner, and keeps which
// strips are open. wakes is how many wake-up rows the chat holds: a new one reads the Runs again at once. waiting
// is how many Approval cards wait above the composer, to decide which strips start open.
export function useDockedWork(chatId: string, wakes: number, waiting: number): DockedWork {
  const queryClient = useQueryClient()
  const list = useQuery({
    ...listRunsOptions({ query: { chat_id: chatId, status: "open" } }),
    select: (page) => page.data,
    refetchInterval: runsEvery,
  })
  // The Runs a wake-up's chip asked to see stay docked, ended or not, until the chat is left.
  const [pinned, setPinned] = useState<string[]>([])
  const read = toRead(
    (list.data ?? []).map((r) => r.id),
    pinned,
  )
  const details = useQueries({
    queries: read.map((id) => ({
      ...getRunOptions({ path: { run_id: id } }),
      refetchInterval: runsEvery,
    })),
  })
  const questions = useQuery({
    ...listQuestionsOptions({ query: { chat_id: chatId, status: "open", addressed_to: "owner" } }),
    select: (page) => page.data,
    refetchInterval: questionsEvery,
  })

  // A wake-up says a Run moved on: read the Runs and the questions now rather than at the next poll.
  useEffect(() => {
    if (wakes === 0) return
    for (const queryKey of [
      listRunsQueryKey({ query: { chat_id: chatId, status: "open" } }),
      everyCall(getRunQueryKey({ path: { run_id: "" } })),
      listQuestionsQueryKey({ query: { chat_id: chatId, status: "open", addressed_to: "owner" } }),
    ])
      void queryClient.invalidateQueries({ queryKey })
  }, [wakes, chatId, queryClient])

  const fetched = details.flatMap((d) => (d.data ? [d.data] : []))
  const tasks = new Map(fetched.flatMap((r) => r.tasks.map((t) => [t.id, t] as const)))
  const { runs, foreign } = docked(fetched, chatId, pinned)
  const asked = (questions.data ?? []).filter(
    (q) => q.status === "open" && q.addressed_to === "owner",
  )

  // Which strips are open: decided once for a Run when it is first seen, with every Run read, then the owner's.
  const [open, setOpenState] = useState<Record<string, boolean>>({})
  const loaded = details.every((d) => d.data !== undefined || d.isError)
  const fresh = loaded
    ? startOpen(runs, open, { height: window.innerHeight, waiting: waiting + asked.length })
    : {}
  if (Object.keys(fresh).length > 0) setOpenState({ ...open, ...fresh })

  const setOpen = (runId: string, next: boolean) => setOpenState((o) => ({ ...o, [runId]: next }))

  // A strip asked for by a chip comes into view once it is drawn, which for a Run not docked yet is after its read.
  // focus is a new object on every press, so a second press on the same chip scrolls again.
  const [focus, setFocus] = useState<{ runId: string }>()
  const drawn = focus !== undefined && runs.some((r) => r.id === focus.runId)
  useEffect(() => {
    if (!drawn || !focus) return
    document
      .getElementById(dockId(chatId, focus.runId))
      ?.scrollIntoView({ block: "nearest", behavior: "smooth" })
  }, [drawn, focus, chatId])

  return {
    runs,
    questions: asked,
    open,
    setOpen,
    dock: {
      docked: (runId) => !foreign.has(runId),
      show: (runId) => {
        setPinned((p) => (p.includes(runId) ? p : [...p, runId]))
        setOpen(runId, true)
        setFocus({ runId })
      },
      task: (taskId) => tasks.get(taskId),
    },
  }
}
