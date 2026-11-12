import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { listLarkChannels } from "@/api/generated/sdk.gen"

export const channelsKey = ["lark-channels"] as const

// whileOpen is how often the Channels are read again while a page shows them: the control plane finds a
// Channel's Lark groups by itself, when the bot is added to one and in its listing every ten minutes.
const whileOpen = 30_000

// useLarkChannels reads the Lark Channels, the oldest first. Only the owner may read them.
export function useLarkChannels() {
  return useQuery({
    queryKey: channelsKey,
    queryFn: async () => (await listLarkChannels()).data.data,
    refetchInterval: whileOpen,
  })
}

export function useRefreshChannels() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: channelsKey })
}
