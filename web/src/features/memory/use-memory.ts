import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getAgentMemoryOptions,
  getAgentMemoryQueryKey,
  getOwnerMemoryOptions,
  getOwnerMemoryQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { setAgentMemory, setOwnerMemory } from "@/api/generated/sdk.gen"

// An agent's memory, shared by all its workers, and the one about the owner, which every worker and Bot gets
// (issue #21). Workers write them while they run, so a page reads them again when it is looked at again.

export function useAgentMemory(name: string) {
  return useQuery(getAgentMemoryOptions({ path: { name } }))
}

export function useOwnerMemory() {
  return useQuery(getOwnerMemoryOptions())
}

// useSetAgentMemory replaces an agent's whole memory with the owner's text, a line an entry. The memory as kept is
// the body its read caches, so it goes in as it is.
export function useSetAgentMemory(name: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (memory: string) =>
      (await setAgentMemory({ path: { name }, body: { memory } })).data,
    onSuccess: (stored) =>
      queryClient.setQueryData(getAgentMemoryQueryKey({ path: { name } }), stored),
  })
}

// useSetOwnerMemory replaces the whole memory about the owner with his text.
export function useSetOwnerMemory() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (memory: string) => (await setOwnerMemory({ body: { memory } })).data,
    onSuccess: (stored) => queryClient.setQueryData(getOwnerMemoryQueryKey(), stored),
  })
}
