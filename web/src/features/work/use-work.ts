import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useRef } from "react"

import "@/api/client"
import {
  getRunOptions,
  getRunQueryKey,
  getTaskOptions,
  listNodesOptions,
  listProfilesOptions,
  listQuestionsOptions,
  listQuestionsQueryKey,
  listRunsOptions,
  listRunsQueryKey,
  listTasksOptions,
  listTasksQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { streamEvents } from "@/api/generated/sdk.gen"
import { isFileQuery, workspaceKey } from "@/features/files/files-state"
import { callsOf } from "@/lib/query-keys"

import { boardCards } from "./work-state"

// whileWorking is how often the Tasks are read again while a worker runs: what a worker does now changes
// without an event.
const whileWorking = 10_000

// useWork reads the work in flight: the Tasks (GET /tasks), the open questions, and the machines and profiles
// that name a card's machine and runtime. LiveUpdates keeps them current, from the cursor this returns as after.
export function useWork() {
  const board = useQuery({
    ...listTasksOptions(),
    refetchInterval: (query) =>
      query.state.data?.data.some((t) => t.status === "running") ? whileWorking : false,
  })
  const questions = useQuery({
    ...listQuestionsOptions({ query: { status: "open" } }),
    select: (page) => page.data,
  })
  const nodes = useQuery({ ...listNodesOptions(), select: (page) => page.data })
  const profiles = useQuery({ ...listProfilesOptions(), select: (page) => page.data })

  const cards = useMemo(
    () => (board.data ? boardCards(board.data, questions.data, nodes.data, profiles.data) : []),
    [board.data, questions.data, nodes.data, profiles.data],
  )

  return { cards, after: board.data?.after, loading: board.isPending, error: board.error }
}

// useRun reads one Run with its Tasks; none until its id is known.
export function useRun(runId: string | undefined, enabled = true) {
  return useQuery({
    ...getRunOptions({ path: { run_id: runId ?? "" } }),
    enabled: enabled && runId !== undefined,
  })
}

// useTask reads one Task, for a Task off the board (an old Run's).
export function useTask(taskId: string, enabled: boolean) {
  return useQuery({ ...getTaskOptions({ path: { task_id: taskId } }), enabled })
}

// useRecentRuns reads the latest Runs, a hundred at most, while enabled.
export function useRecentRuns(enabled: boolean) {
  return useQuery({
    ...listRunsOptions({ query: { limit: 100 } }),
    select: (page) => page.data,
    enabled,
  })
}

// everyWorkRead matches every cached read of the work an event may change: the Tasks, the questions, the lists of
// Runs and each Run, which the hand-written ["board"], ["questions"], ["runs"] and ["run"] prefixes covered.
const everyWorkRead = callsOf(
  listTasksQueryKey(),
  listQuestionsQueryKey(),
  listRunsQueryKey(),
  getRunQueryKey({ path: { run_id: "" } }),
)

// reopenAfter is the pause before the stream is opened again once the server ended it (a restart).
const reopenAfter = 2000
// settle gathers a burst of events (a Run's Tasks created together) into one refresh.
const settle = 250

// useLiveUpdates follows GET /events/stream from the Tasks' own cursor, so nothing between the snapshot and the
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
        void queryClient.invalidateQueries({ predicate: everyWorkRead })
        // A chat's folders, as Omnigent refreshes them when its runner says files changed; a file that is open
        // reads itself again.
        void queryClient.invalidateQueries({
          queryKey: workspaceKey,
          predicate: (query) => !isFileQuery(query.queryKey),
        })
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
