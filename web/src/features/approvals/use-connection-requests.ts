import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listConnectionRequestsOptions,
  listConnectionRequestsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { decideConnectionRequest } from "@/api/generated/sdk.gen"
import type { ConnectionRequest, ConnectionRequestDecision } from "@/api/generated/types.gen"
import { useRefreshBots } from "@/features/bots/use-bots"
import { everyCall } from "@/lib/query-keys"

// A Bot's requests for a Connection it was not given (issue #471, part D, the grant card), read again every few
// seconds as the Approvals are (use-approvals.ts).
const everyMs = 3000

// usePendingConnectionRequests lists the requests that wait for the owner: everywhere, or in one chat.
export function usePendingConnectionRequests(chatId?: string) {
  return useQuery({
    ...listConnectionRequestsOptions({ query: { state: "pending", chat_id: chatId } }),
    select: (page) => page.data,
    refetchInterval: everyMs,
  })
}

// useDecideConnectionRequest answers one request; every list of them, and the Bots, whose lists Always changes, are
// read again.
export function useDecideConnectionRequest(request: ConnectionRequest) {
  const queryClient = useQueryClient()
  const refreshBots = useRefreshBots()

  return useMutation({
    mutationFn: async (decision: ConnectionRequestDecision["decision"]) =>
      (await decideConnectionRequest({ path: { request_id: request.id }, body: { decision } }))
        .data,
    onSettled: () => {
      void refreshBots()
      return queryClient.invalidateQueries({
        queryKey: everyCall(listConnectionRequestsQueryKey()),
      })
    },
  })
}
