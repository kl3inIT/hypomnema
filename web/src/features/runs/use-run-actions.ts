import { useMutation, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getRunQueryKey,
  listRunsQueryKey,
  listTasksQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { cancelRun, finishRun } from "@/api/generated/sdk.gen"
import type { TaskDetail } from "@/api/generated/types.gen"
import { callsOf } from "@/lib/query-keys"

// everyRunRead matches every cached read of the Runs and the Tasks: each Run, the lists of Runs and the Tasks.
const everyRunRead = callsOf(
  getRunQueryKey({ path: { run_id: "" } }),
  listRunsQueryKey(),
  listTasksQueryKey(),
)

// useRunActions finishes or cancels one Run, for the Run docked in its chat (issue #314). Every
// view of Runs and Tasks is read again after either.
export function useRunActions(runId: string) {
  const queryClient = useQueryClient()

  const refresh = () => void queryClient.invalidateQueries({ predicate: everyRunRead })
  const finish = useMutation({
    mutationFn: () => finishRun({ path: { run_id: runId } }),
    onSuccess: refresh,
  })
  const cancel = useMutation({
    mutationFn: () => cancelRun({ path: { run_id: runId } }),
    onSuccess: refresh,
  })

  return { finish, cancel }
}

const ended = new Set<TaskDetail["status"]>(["succeeded", "failed", "canceled"])

// allEnded says whether a Run can be finished: the server refuses while a Task has not ended.
export function allEnded(tasks: TaskDetail[]) {
  return tasks.every((t) => ended.has(t.status))
}
