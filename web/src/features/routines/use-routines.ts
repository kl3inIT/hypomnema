import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useEffect, useState } from "react"

import "@/api/client"
import {
  getRoutineOptions,
  getRoutineQueryKey,
  listRoutineRunsInfiniteOptions,
  listRoutineRunsInfiniteQueryKey,
  listRoutinesOptions,
  listRoutinesQueryKey,
  previewRoutineScheduleOptions,
  previewRoutineScheduleQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  deleteRoutine,
  patchRoutine,
  pauseRoutine,
  putRoutine,
  resumeRoutine,
  revokeRoutineGrant,
  runRoutine,
  undoRoutine,
} from "@/api/generated/sdk.gen"
import type {
  Routine,
  RoutineAccess,
  RoutineGrant,
  RoutineSchedule,
  RoutineSettings,
} from "@/api/generated/types.gen"
import { toast } from "@/components/ui/toast"
import { callsOf } from "@/lib/query-keys"

// Routines (issue #211): work a Bot does on a schedule. They change by themselves (a run starts, ends, waits),
// without an event, so a page that shows them reads them again every few seconds, as the Bots pages do.
const whileOpen = 5000

// runsPage is how many runs a page of a Routine's runs holds.
const runsPage = 20

// useRoutines lists the Routines of a Bot, or those made in a chat; every Routine when neither is given.
export function useRoutines(where: { bot?: string; chatId?: string } = {}) {
  return useQuery({
    ...listRoutinesOptions({ query: { bot: where.bot, chat_id: where.chatId } }),
    select: (page) => page.data,
    refetchInterval: whileOpen,
  })
}

// useRoutine reads one Routine, with a script's text and its grants.
export function useRoutine(id: string | undefined) {
  return useQuery({
    ...getRoutineOptions({ path: { routine_id: id ?? "" } }),
    enabled: Boolean(id),
    refetchInterval: whileOpen,
    retry: false,
  })
}

// useRoutineRuns reads a Routine's runs a page at a time, the newest first, as OpenClaw's run history pages them
// (src/gateway/server-methods/cron-history.ts: offset, limit, hasMore and nextOffset). The generated infinite
// options ask each page with its offset beside the limit in the key.
export function useRoutineRuns(id: string | undefined) {
  return useInfiniteQuery({
    ...listRoutineRunsInfiniteOptions({
      path: { routine_id: id ?? "" },
      query: { limit: runsPage },
    }),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_offset,
    enabled: Boolean(id),
    refetchInterval: whileOpen,
    retry: false,
  })
}

// useSchedulePreview asks the server for a schedule in words and its next three times, a moment after the owner
// stops typing. The answer before stays on the screen while the next is asked. The preview is a read sent as a
// POST, so its generated key carries the schedule asked (web/openapi-ts.config.ts).
export function useSchedulePreview(schedule: RoutineSchedule) {
  const key = JSON.stringify(schedule)
  const [asked, setAsked] = useState(key)

  useEffect(() => {
    const timer = setTimeout(() => setAsked(key), 300)

    return () => clearTimeout(timer)
  }, [key])

  return useQuery({
    ...previewRoutineScheduleOptions({ body: JSON.parse(asked) as RoutineSchedule }),
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: 30_000,
  })
}

// everyRoutineRead matches every cached read of the Routines, whatever its Bot, chat, Routine or page: their lists,
// one Routine, its runs and a schedule's preview, which the hand-written ["routines"] prefix covered.
const everyRoutineRead = callsOf(
  listRoutinesQueryKey(),
  getRoutineQueryKey({ path: { routine_id: "" } }),
  listRoutineRunsInfiniteQueryKey({ path: { routine_id: "" } }),
  previewRoutineScheduleQueryKey({ body: {} }),
)

// What the owner does to a Routine. After each, every list and page of Routines is read again. The mutations keep
// a mutationFn over the SDK: their variables are the page's own (a Routine, a Bot and a name, a grant).
function useChange<A, T>(run: (arg: A) => Promise<T>, onSuccess?: (done: T) => void) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: run,
    onSuccess,
    onSettled: () => queryClient.invalidateQueries({ predicate: everyRoutineRead }),
  })
}

// usePutRoutine makes a Routine of a Bot, or changes the one of that name.
export function usePutRoutine() {
  return useChange(
    async ({ bot, name, settings }: { bot: string; name: string; settings: RoutineSettings }) =>
      (await putRoutine({ path: { bot_id: bot, routine: name }, body: settings })).data,
  )
}

// useSwitch pauses a Routine that is on and resumes one that is paused.
export function useSwitch() {
  return useChange(async (r: Routine) =>
    r.state === "on"
      ? (await pauseRoutine({ path: { routine_id: r.id } })).data
      : (await resumeRoutine({ path: { routine_id: r.id } })).data,
  )
}

export function useRoutineAccess() {
  return useChange(
    async ({ id, access }: { id: string; access: RoutineAccess }) =>
      (await patchRoutine({ path: { routine_id: id }, body: { access } })).data,
  )
}

// useRunNow starts a run by hand; the server refuses it while another run still goes or waits.
export function useRunNow() {
  return useChange(async (r: Routine) => (await runRoutine({ path: { routine_id: r.id } })).data)
}

// useRemove removes a Routine; a run still going is stopped.
export function useRemove() {
  return useChange(async (r: Routine) => {
    await deleteRoutine({ path: { routine_id: r.id } })
  })
}

// useUndo takes back the Bot's last change: a Routine it made is removed, one it changed is put back.
// Its toast is the hook's, not the card's: a Routine the undo removes takes its card away with it.
export function useUndo() {
  return useChange(
    async (r: Routine) => (await undoRoutine({ path: { routine_id: r.id } })).data,
    (done) =>
      toast.add({
        title: done.removed ? "Routine removed · undone" : "Routine put back · undone",
        type: "success",
      }),
  )
}

// useRevoke takes back an "always for this Routine".
export function useRevoke() {
  return useChange(async ({ routine, grant }: { routine: Routine; grant: RoutineGrant }) => {
    await revokeRoutineGrant({
      path: {
        routine_id: routine.id,
        integration: grant.integration,
        label: grant.label,
        action: grant.action,
      },
    })
  })
}
