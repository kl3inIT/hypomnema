import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { listChats } from "@/api/generated/sdk.gen"

export const chatsKey = ["chats"] as const

// useChats reads the chats; a page that shows whether a chat is working reads them again every `every`
// milliseconds while its tab is visible (the Team page, as OpenMausBot's team map polls every 3 s).
export function useChats({ every }: { every?: number } = {}) {
  return useQuery({
    queryKey: chatsKey,
    queryFn: async () => (await listChats()).data.data,
    refetchInterval: every ?? false,
  })
}
