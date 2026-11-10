import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listMachineProviders,
  listNodes,
  startNodeMachine,
  stopNodeMachine,
} from "@/api/generated/sdk.gen"
import type { Node } from "@/api/generated/types.gen"

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

// useMachinePower turns a provider's machine off or on. A machine that is off keeps its disk; the server refuses
// to turn off one that still runs a Dispatch, and says so.
export function useMachinePower(node: Node) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (on: boolean) =>
      on
        ? await startNodeMachine({ path: { node_id: node.id } })
        : await stopNodeMachine({ path: { node_id: node.id } }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: nodesKey }),
  })
}
