import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { listNodes } from "@/api/generated/sdk.gen"

export const nodesKey = ["nodes"] as const

// whileOpen is how often the machines are read again while the page is visible: a node's measurements change
// without an event.
const whileOpen = 5000

// useMachines reads GET /nodes and keeps it current while the page is open and its tab is visible.
export function useMachines() {
  return useQuery({
    queryKey: nodesKey,
    queryFn: async () => (await listNodes()).data.data,
    refetchInterval: whileOpen,
  })
}
