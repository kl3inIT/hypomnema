import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { listLarkChannels } from "@/api/generated/sdk.gen"

export const channelsKey = ["lark-channels"] as const

// useLarkChannels reads the Lark Channels, the oldest first. Only the owner may read them.
export function useLarkChannels() {
  return useQuery({
    queryKey: channelsKey,
    queryFn: async () => (await listLarkChannels()).data.data,
  })
}

export function useRefreshChannels() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: channelsKey })
}
