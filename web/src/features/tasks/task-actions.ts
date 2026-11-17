import { useMutation, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getRunQueryKey,
  getTaskQueryKey,
  listQuestionsQueryKey,
  listRunsQueryKey,
  listTasksQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { cancelTask } from "@/api/generated/sdk.gen"
import { callsOf } from "@/lib/query-keys"

// taskReads matches what the owner's act on a Task changes: the Tasks, the questions, the Runs and each Run, and
// each Task.
const taskReads = callsOf(
  listTasksQueryKey(),
  listQuestionsQueryKey(),
  listRunsQueryKey(),
  getRunQueryKey({ path: { run_id: "" } }),
  getTaskQueryKey({ path: { task_id: "" } }),
)

// useRefreshTasks reads every view of the work again after the owner acted on a Task.
export function useRefreshTasks() {
  const queryClient = useQueryClient()

  return () => void queryClient.invalidateQueries({ predicate: taskReads })
}

// useCancelTask cancels one Task (POST /tasks/{id}/cancel): its worker is stopped, from its panel or its row in the
// chat's dock (issue #687).
export function useCancelTask(taskId: string) {
  const refresh = useRefreshTasks()

  return useMutation({
    mutationFn: () => cancelTask({ path: { task_id: taskId } }),
    onSuccess: refresh,
  })
}
