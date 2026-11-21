import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getProfileOptions,
  getProfileQueryKey,
  listModelsOptions,
  listProfilesOptions,
  listProfilesQueryKey,
  listProfileVersionsOptions,
  listProfileVersionsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { applyProfile, deleteProfile, restoreProfileVersion } from "@/api/generated/sdk.gen"
import { callsOf } from "@/lib/query-keys"

// whileOpen is how often the profiles are read again while a page shows them: whether a worker can start changes
// with the machines and the accounts, without an event.
const whileOpen = 10_000

// useProfiles reads every profile at its current version, with whether a worker can start under it now.
export function useProfiles() {
  return useQuery({
    ...listProfilesOptions(),
    select: (page) => page.data,
    refetchInterval: whileOpen,
  })
}

// useProfile reads one profile at its current version, while `enabled`.
export function useProfile(name: string, enabled = true) {
  return useQuery({
    ...getProfileOptions({ path: { name } }),
    refetchInterval: whileOpen,
    retry: false,
    enabled,
  })
}

// useProfileVersions reads a profile's versions, newest first, while `enabled`.
export function useProfileVersions(name: string, enabled: boolean) {
  return useQuery({
    ...listProfileVersionsOptions({ path: { name } }),
    select: (page) => page.data,
    enabled,
  })
}

// useModels reads the models the providers offer, for the builder's model field.
export function useModels() {
  return useQuery({
    ...listModelsOptions(),
    select: (page) => page.data,
    staleTime: 60_000,
  })
}

// useRefreshProfiles reads the profiles again after one changes: every cached read of their list, of one profile
// and of a profile's versions, which the hand-written ["profiles"] prefix covered.
export function useRefreshProfiles() {
  const queryClient = useQueryClient()

  return () =>
    queryClient.invalidateQueries({
      predicate: callsOf(
        listProfilesQueryKey(),
        getProfileQueryKey({ path: { name: "" } }),
        listProfileVersionsQueryKey({ path: { name: "" } }),
      ),
    })
}

// useApplyProfile stores a profile's YAML: a new version, or the current one when nothing changed.
export function useApplyProfile() {
  const refresh = useRefreshProfiles()

  return useMutation({
    mutationFn: async ({ name, source }: { name: string; source: string }) =>
      (await applyProfile({ path: { name }, body: { source } })).data,
    onSuccess: refresh,
  })
}

// useRestoreProfileVersion applies one of a profile's versions again by its id, so the server uses that version's own
// source and the provider it recorded for a model its YAML names without one (issue #1002).
export function useRestoreProfileVersion() {
  const refresh = useRefreshProfiles()

  return useMutation({
    mutationFn: async ({ name, versionId }: { name: string; versionId: string }) =>
      (await restoreProfileVersion({ path: { name, version_id: versionId } })).data,
    onSuccess: refresh,
  })
}

// useDeleteProfile deletes a profile (issue #485): one never used goes, one used before is hidden and comes back when
// a profile of its name is saved. The server refuses implementer and one still named by an unfinished Task, another
// profile's fallbacks or a Bot's workers.
export function useDeleteProfile() {
  return useMutation({
    mutationFn: async (name: string) => {
      await deleteProfile({ path: { name } })
    },
  })
}
