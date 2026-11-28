import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getProfileMemoryOptions,
  getProfileMemoryQueryKey,
  getOwnerMemoryOptions,
  getOwnerMemoryQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { setOwnerMemory, setProfileMemory } from "@/api/generated/sdk.gen"

// A profile's memory, shared by all its workers (the web's Agents are the API's profiles, issue #1336), and the one
// about the owner, which every worker and Bot gets (issue #21). Workers write them while they run, so a page reads them again when it is looked at again.

export function useProfileMemory(name: string) {
  return useQuery(getProfileMemoryOptions({ path: { name } }))
}

export function useOwnerMemory() {
  return useQuery(getOwnerMemoryOptions())
}

// useSetProfileMemory replaces a profile's whole memory with the owner's text, a line an entry. The memory as kept is
// the body its read caches, so it goes in as it is.
export function useSetProfileMemory(name: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (memory: string) =>
      (await setProfileMemory({ path: { name }, body: { memory } })).data,
    onSuccess: (stored) =>
      queryClient.setQueryData(getProfileMemoryQueryKey({ path: { name } }), stored),
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
