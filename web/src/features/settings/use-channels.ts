import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listLarkChannelsOptions,
  listLarkChannelsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { everyCall } from "@/lib/query-keys"

// whileOpen is how often the Channels are read again while a page shows them: the control plane finds a
// Channel's Lark groups by itself, when the bot is added to one and in its listing every ten minutes.
const whileOpen = 30_000

// useLarkChannels reads the Lark Channels, the oldest first. Only the owner may read them.
export function useLarkChannels() {
  return useQuery({
    ...listLarkChannelsOptions(),
    select: (page) => page.data,
    refetchInterval: whileOpen,
  })
}

// useRefreshChannels reads the Channels again after one changes: every cached read of their list.
export function useRefreshChannels() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listLarkChannelsQueryKey()) })
}
