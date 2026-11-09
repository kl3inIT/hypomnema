import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { listChats } from "@/api/generated/sdk.gen"

export const chatsKey = ["chats"] as const

export function useChats() {
  return useQuery({ queryKey: chatsKey, queryFn: async () => (await listChats()).data.data })
}
