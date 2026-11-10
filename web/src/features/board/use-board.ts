import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useRef } from "react"

import "@/api/client"
import {
  listNodes,
  listProfiles,
  listQuestions,
  listTasks,
  streamEvents,
} from "@/api/generated/sdk.gen"

import { boardCards } from "./board-state"

export const boardKey = ["board"] as const
export const questionsKey = ["questions"] as const

// whileWorking is how often the board is read again while a worker runs: what a worker does now changes
// without an event.
const whileWorking = 10_000

// useBoard reads the work in flight: the Tasks (GET /tasks), the open questions, and the machines and profiles
// that name a card's machine and runtime. LiveUpdates keeps them current.
export function useBoard() {
  const board = useQuery({
    queryKey: boardKey,
    queryFn: async () => (await listTasks()).data,
    refetchInterval: (query) =>
      query.state.data?.data.some((t) => t.status === "running") ? whileWorking : false,
  })
  const questions = useQuery({
    queryKey: [...questionsKey, "open"],
    queryFn: async () => (await listQuestions({ query: { status: "open" } })).data.data,
  })
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: async () => (await listNodes()).data.data,
  })
  const profiles = useQuery({
    queryKey: ["profiles"],
    queryFn: async () => (await listProfiles()).data.data,
  })

  const cards = useMemo(
    () => (board.data ? boardCards(board.data, questions.data, nodes.data, profiles.data) : []),
    [board.data, questions.data, nodes.data, profiles.data],
  )

  return { cards, after: board.data?.after, loading: board.isPending, error: board.error }
}

// reopenAfter is the pause before the stream is opened again once the server ended it (a restart).
const reopenAfter = 2000
// settle gathers a burst of events (a Run's Tasks created together) into one refresh.
const settle = 250

// useLiveUpdates follows GET /events/stream from the board's own cursor, so nothing between the snapshot and the
// stream is missed and no history is replayed (Omnara's agent-event-stream follows the same way), and refreshes
// what an event may have changed. The generated client reconnects with Last-Event-ID after a network error.
export function useLiveUpdates(after: number | undefined) {
  const queryClient = useQueryClient()
  const started = useRef(false)

  useEffect(() => {
    if (after === undefined || started.current) return
    started.current = true

    const controller = new AbortController()
    let cursor = after
    let timer: ReturnType<typeof setTimeout> | undefined

    const refresh = () => {
      clearTimeout(timer)
      timer = setTimeout(() => {
        for (const queryKey of [boardKey, questionsKey, ["runs"], ["run"]]) {
          void queryClient.invalidateQueries({ queryKey })
        }
      }, settle)
    }

    async function follow() {
      while (!controller.signal.aborted) {
        try {
          const { stream } = await streamEvents({
            query: { after: cursor },
            signal: controller.signal,
            onSseEvent: (e) => {
              if (!e.id) return
              cursor = Math.max(cursor, Number(e.id))
              refresh()
            },
          })

          for await (const _ of stream) {
            // Events are handled in onSseEvent, where their ids are known.
          }
        } catch {
          // Reopened below.
        }

        await new Promise((resolve) => setTimeout(resolve, reopenAfter))
      }
    }

    void follow()

    return () => {
      started.current = false
      clearTimeout(timer)
      controller.abort()
    }
  }, [after, queryClient])
}
