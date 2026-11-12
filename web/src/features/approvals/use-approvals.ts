import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { decideApproval, listApprovals } from "@/api/generated/sdk.gen"
import type { Approval, ApprovalDecision } from "@/api/generated/types.gen"

// Approvals (issue #154): requests agents made to the owner's accounts that wait for his answer. The list is
// read again every few seconds while a screen that shows it is open, as Onyx reads a session's approvals.
export const approvalsKey = ["approvals"] as const

const everyMs = 3000

// usePending lists what waits for the owner: everywhere, in one chat, for one Task, or in one Routine's runs.
export function usePending(where: { chatId?: string; taskId?: string; routineId?: string } = {}) {
  return useQuery({
    queryKey: [
      ...approvalsKey,
      "pending",
      where.chatId ?? "",
      where.taskId ?? "",
      where.routineId ?? "",
    ],
    queryFn: async () =>
      (
        await listApprovals({
          query: {
            status: "pending",
            chat_id: where.chatId,
            task_id: where.taskId,
            routine_id: where.routineId,
          },
        })
      ).data.data,
    refetchInterval: everyMs,
  })
}

// useDecide answers one Approval; every list of them is read again.
export function useDecide(approval: Approval) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (decision: ApprovalDecision["decision"]) =>
      (await decideApproval({ path: { approval_id: approval.id }, body: { decision } })).data,
    onSettled: () => queryClient.invalidateQueries({ queryKey: approvalsKey }),
  })
}

// sessionOf names what "for the session" means for an Approval: the chat or the Run that asked.
export function sessionOf(approval: Approval): string | null {
  if (approval.chat_id) return "this chat"
  if (approval.run_id) return "this Run"

  return null
}

// readable turns a request's own field name (a JSON key such as "emailAddress" or "send_notification") into words
// ("Email address"), as the mail's fields already read (From, To, Subject). A label the server wrote for people,
// which starts with a capital, stays as it is; the raw name stays in the label's tooltip.
export function readable(label: string) {
  if (!/^[a-z]/.test(label)) return label

  const words = label
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[-_.]+/g, " ")
    .trim()
    .toLowerCase()

  return words.charAt(0).toUpperCase() + words.slice(1)
}
