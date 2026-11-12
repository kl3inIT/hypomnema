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
  deleteRoutine,
  getRoutine,
  listRoutineRuns,
  listRoutines,
  pauseRoutine,
  previewRoutineSchedule,
  putRoutine,
  resumeRoutine,
  revokeRoutineGrant,
  runRoutine,
  undoRoutine,
} from "@/api/generated/sdk.gen"
import type {
  Routine,
  RoutineGrant,
  RoutineSchedule,
  RoutineSettings,
} from "@/api/generated/types.gen"
import { toast } from "@/components/ui/toast"

// Routines (issue #211): work a Bot does on a schedule. They change by themselves (a run starts, ends, waits),
// without an event, so a page that shows them reads them again every few seconds, as the Bots pages do.
export const routinesKey = ["routines"] as const

const whileOpen = 5000

// runsPage is how many runs a page of a Routine's runs holds.
export const runsPage = 20

// useRoutines lists the Routines of a Bot, or those made in a chat; every Routine when neither is given.
export function useRoutines(where: { bot?: string; chatId?: string } = {}) {
  return useQuery({
    queryKey: [...routinesKey, "list", where.bot ?? "", where.chatId ?? ""],
    queryFn: async () =>
      (await listRoutines({ query: { bot: where.bot, chat_id: where.chatId } })).data.data,
    refetchInterval: whileOpen,
  })
}

// useRoutine reads one Routine, with a script's text and its grants.
export function useRoutine(id: string | undefined) {
  return useQuery({
    queryKey: [...routinesKey, "one", id ?? ""],
    queryFn: async () => (await getRoutine({ path: { routine_id: id ?? "" } })).data,
    enabled: Boolean(id),
    refetchInterval: whileOpen,
    retry: false,
  })
}

// useRoutineRuns reads a Routine's runs a page at a time, the newest first, as OpenClaw's run history pages them
// (src/gateway/server-methods/cron-history.ts: offset, limit, hasMore and nextOffset).
export function useRoutineRuns(id: string | undefined) {
  return useInfiniteQuery({
    queryKey: [...routinesKey, "runs", id ?? ""],
    queryFn: async ({ pageParam }) =>
      (
        await listRoutineRuns({
          path: { routine_id: id ?? "" },
          query: { limit: runsPage, offset: pageParam },
        })
      ).data,
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_offset,
    enabled: Boolean(id),
    refetchInterval: whileOpen,
    retry: false,
  })
}

// useSchedulePreview asks the server for a schedule in words and its next three times, a moment after the owner
// stops typing. The answer before stays on the screen while the next is asked.
export function useSchedulePreview(schedule: RoutineSchedule) {
  const key = JSON.stringify(schedule)
  const [asked, setAsked] = useState(key)

  useEffect(() => {
    const timer = setTimeout(() => setAsked(key), 300)

    return () => clearTimeout(timer)
  }, [key])

  return useQuery({
    queryKey: [...routinesKey, "preview", asked],
    queryFn: async () =>
      (await previewRoutineSchedule({ body: JSON.parse(asked) as RoutineSchedule })).data,
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: 30_000,
  })
}

// What the owner does to a Routine. After each, every list and page of Routines is read again.
function useChange<A, T>(run: (arg: A) => Promise<T>, onSuccess?: (done: T) => void) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: run,
    onSuccess,
    onSettled: () => queryClient.invalidateQueries({ queryKey: routinesKey }),
  })
}

// usePutRoutine makes a Routine of a Bot, or changes the one of that name.
export function usePutRoutine() {
  return useChange(
    async ({ bot, name, settings }: { bot: string; name: string; settings: RoutineSettings }) =>
      (await putRoutine({ path: { name: bot, routine: name }, body: settings })).data,
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
