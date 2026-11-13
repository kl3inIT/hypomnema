import { useMutation, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { cancelRun, finishRun } from "@/api/generated/sdk.gen"
import type { TaskDetail } from "@/api/generated/types.gen"
import { boardKey } from "@/features/work/use-work"

// useRunActions finishes or cancels one Run, for the Run docked in its chat (issue #314). Every
// view of Runs and Tasks is read again after either.
export function useRunActions(runId: string) {
  const queryClient = useQueryClient()

  const refresh = () => {
    for (const queryKey of [["run"], ["runs"], boardKey])
      void queryClient.invalidateQueries({ queryKey })
  }
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
