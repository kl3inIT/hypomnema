import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listMachineProvidersOptions,
  listMachineProvidersQueryKey,
  listNodesOptions,
  listNodesQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { startNodeMachine, stopNodeMachine } from "@/api/generated/sdk.gen"
import type { Node } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

// useMachineProviders reads the providers a machine can be created at while `enabled`. A token that may not read
// them, or a server that has none, leaves the list empty, and the page offers only the owner's own machines.
export function useMachineProviders(enabled: boolean) {
  return useQuery({
    ...listMachineProvidersOptions(),
    select: (page) => page.data,
    enabled,
    retry: false,
  })
}

// useRefreshMachineProviders reads the providers again after a machine is made at one: every cached read of
// their list.
export function useRefreshMachineProviders() {
  const queryClient = useQueryClient()

  return () =>
    queryClient.invalidateQueries({ queryKey: everyCall(listMachineProvidersQueryKey()) })
}

// whileOpen is how often the machines are read again while the page is visible: a node's measurements change
// without an event.
const whileOpen = 5000

// useMachines reads GET /nodes and keeps it current while the page is open and its tab is visible.
export function useMachines() {
  return useQuery({
    ...listNodesOptions(),
    select: (page) => page.data,
    refetchInterval: whileOpen,
  })
}

// useRefreshMachines reads the machines again after one changes: every cached read of their list.
export function useRefreshMachines() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listNodesQueryKey()) })
}

// useMachinePower turns a provider's machine off or on. A machine that is off keeps its disk; the server refuses
// to turn off one that still runs a Dispatch, and says so.
export function useMachinePower(node: Node) {
  const refresh = useRefreshMachines()

  return useMutation({
    mutationFn: async (on: boolean) =>
      on
        ? await startNodeMachine({ path: { node_id: node.id } })
        : await stopNodeMachine({ path: { node_id: node.id } }),
    onSettled: refresh,
  })
}
