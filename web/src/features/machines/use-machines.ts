import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { listMachineProviders, listNodes } from "@/api/generated/sdk.gen"

export const nodesKey = ["nodes"] as const
export const providersKey = ["machine-providers"] as const

// useMachineProviders reads the providers a machine can be created at while `enabled`. A token that may not read
// them, or a server that has none, leaves the list empty, and the page offers only the owner's own machines.
export function useMachineProviders(enabled: boolean) {
  return useQuery({
    queryKey: providersKey,
    queryFn: async () => (await listMachineProviders()).data.data,
    enabled,
    retry: false,
  })
}

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
