import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getAgentMemory,
  getOwnerMemory,
  setAgentMemory,
  setOwnerMemory,
} from "@/api/generated/sdk.gen"

// An agent's memory, shared by all its workers, and the one about the owner, which every worker and Bot gets
// (issue #21). Workers write them while they run, so a page reads them again when it is looked at again.

export const agentMemoryKey = (name: string) => ["agent-memory", name] as const
export const ownerMemoryKey = ["owner-memory"] as const

export function useAgentMemory(name: string) {
  return useQuery({
    queryKey: agentMemoryKey(name),
    queryFn: async () => (await getAgentMemory({ path: { name } })).data,
  })
}

export function useOwnerMemory() {
  return useQuery({
    queryKey: ownerMemoryKey,
    queryFn: async () => (await getOwnerMemory()).data,
  })
}

// useSetAgentMemory replaces an agent's whole memory with the owner's text, a line an entry.
export function useSetAgentMemory(name: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (memory: string) =>
      (await setAgentMemory({ path: { name }, body: { memory } })).data,
    onSuccess: (stored) => queryClient.setQueryData(agentMemoryKey(name), stored),
  })
}

// useSetOwnerMemory replaces the whole memory about the owner with his text.
export function useSetOwnerMemory() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (memory: string) => (await setOwnerMemory({ body: { memory } })).data,
    onSuccess: (stored) => queryClient.setQueryData(ownerMemoryKey, stored),
  })
}
