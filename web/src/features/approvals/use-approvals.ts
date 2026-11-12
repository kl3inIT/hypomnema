import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { decideApproval, listApprovals } from "@/api/generated/sdk.gen"
import type { Approval, ApprovalDecision } from "@/api/generated/types.gen"

// Approvals (issue #154): requests agents made to the owner's accounts that wait for his answer. The list is
// read again every few seconds while a screen that shows it is open, as Onyx reads a session's approvals.
export const approvalsKey = ["approvals"] as const

const everyMs = 3000

// usePending lists what waits for the owner: everywhere, in one chat, or for one Task.
export function usePending(where: { chatId?: string; taskId?: string } = {}) {
  return useQuery({
    queryKey: [...approvalsKey, "pending", where.chatId ?? "", where.taskId ?? ""],
    queryFn: async () =>
      (
        await listApprovals({
          query: { status: "pending", chat_id: where.chatId, task_id: where.taskId },
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
