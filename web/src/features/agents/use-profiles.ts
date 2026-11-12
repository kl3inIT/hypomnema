import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  applyProfile,
  getProfile,
  listModels,
  listProfiles,
  listProfileVersions,
} from "@/api/generated/sdk.gen"

export const profilesKey = ["profiles"] as const

// whileOpen is how often the profiles are read again while a page shows them: whether a worker can start changes
// with the machines and the accounts, without an event.
const whileOpen = 10_000

// useProfiles reads every profile at its current version, with whether a worker can start under it now.
export function useProfiles() {
  return useQuery({
    queryKey: profilesKey,
    queryFn: async () => (await listProfiles()).data.data,
    refetchInterval: whileOpen,
  })
}

// useProfile reads one profile at its current version, while `enabled`.
export function useProfile(name: string, enabled = true) {
  return useQuery({
    queryKey: [...profilesKey, name],
    queryFn: async () => (await getProfile({ path: { name } })).data,
    refetchInterval: whileOpen,
    retry: false,
    enabled,
  })
}

// useProfileVersions reads a profile's versions, newest first, while `enabled`.
export function useProfileVersions(name: string, enabled: boolean) {
  return useQuery({
    queryKey: [...profilesKey, name, "versions"],
    queryFn: async () => (await listProfileVersions({ path: { name } })).data.data,
    enabled,
  })
}

// useModels reads the models the providers offer, for the builder's model field.
export function useModels() {
  return useQuery({
    queryKey: ["models"],
    queryFn: async () => (await listModels()).data.data,
    staleTime: 60_000,
  })
}

// useApplyProfile stores a profile's YAML: a new version, or the current one when nothing changed.
export function useApplyProfile() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ name, source }: { name: string; source: string }) =>
      (await applyProfile({ path: { name }, body: { source } })).data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: profilesKey }),
  })
}
