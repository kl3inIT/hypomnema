import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getSkillOptions,
  getSkillQueryKey,
  listBuiltinSkillsOptions,
  listSkillAuditsOptions,
  listSkillFilesOptions,
  listSkillFilesQueryKey,
  listSkillsOptions,
  listSkillsQueryKey,
  searchSkillsOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import { useProfiles } from "@/features/agents/use-profiles"
import { useBots } from "@/features/bots/use-bots"
import { callsOf } from "@/lib/query-keys"

// The skills the owner added and those the binary ships (issue #21), with the profiles and bots that name them.

export function useSkills() {
  return useQuery({ ...listSkillsOptions(), select: (page) => page.data })
}

export function useBuiltinSkills() {
  return useQuery({
    ...listBuiltinSkillsOptions(),
    select: (page) => page.data,
    staleTime: Infinity,
  })
}

export function useSkill(name: string) {
  return useQuery(getSkillOptions({ path: { skill: name } }))
}

export function useSkillFiles(name: string, revision: number | undefined) {
  return useQuery({
    ...listSkillFilesOptions({ path: { skill: name, revision: revision ?? 0 } }),
    select: (page) => page.data,
    enabled: revision !== undefined,
    staleTime: Infinity,
  })
}

// useRefreshSkills reads the skills again after one changes: every cached read of their list, of one skill and of
// a revision's files, which the hand-written ["skills"] prefix covered.
export function useRefreshSkills() {
  const queryClient = useQueryClient()

  return () =>
    queryClient.invalidateQueries({
      predicate: callsOf(
        listSkillsQueryKey(),
        getSkillQueryKey({ path: { skill: "" } }),
        listSkillFilesQueryKey({ path: { skill: "", revision: 0 } }),
      ),
    })
}

// useNamers are the profiles and bots, which say who adds a skill and who turns one off.
export function useNamers() {
  const profiles = useProfiles()
  const bots = useBots()

  return { profiles: profiles.data ?? [], bots: bots.data ?? [] }
}

// useSkillSearch asks skills.sh, through the control plane, once the query has two characters, as the skills CLI's
// search prompt does (src/find.ts); the last results stay while the next are asked.
export function useSkillSearch(query: string) {
  const q = query.trim()

  return useQuery({
    ...searchSkillsOptions({ query: { q } }),
    enabled: q.length >= 2,
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  })
}

// useSkillAudits is what skills.sh's auditors said of a result, read when the owner picks it.
export function useSkillAudits(source: string | undefined, slug: string | undefined) {
  return useQuery({
    ...listSkillAuditsOptions({ query: { source: source ?? "", skill: slug ?? "" } }),
    select: (page) => page.data,
    enabled: !!source && !!slug,
    staleTime: Infinity,
  })
}
