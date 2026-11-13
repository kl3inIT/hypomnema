import { keepPreviousData, useQuery } from "@tanstack/react-query"

import "@/api/client"
import {
  getSkill,
  listBuiltinSkills,
  listSkillAudits,
  listSkillFiles,
  listSkills,
  searchSkills,
} from "@/api/generated/sdk.gen"
import { useProfiles } from "@/features/agents/use-profiles"
import { useBots } from "@/features/bots/use-bots"

// The skills the owner added and those the binary ships (issue #21), with the profiles and bots that name them.

export const skillsKey = ["skills"] as const
export const builtinSkillsKey = ["built-in-skills"] as const
export const skillKey = (name: string) => ["skills", name] as const
export const skillFilesKey = (name: string, revision: number) =>
  ["skills", name, "files", revision] as const

export function useSkills() {
  return useQuery({ queryKey: skillsKey, queryFn: async () => (await listSkills()).data.data })
}

export function useBuiltinSkills() {
  return useQuery({
    queryKey: builtinSkillsKey,
    queryFn: async () => (await listBuiltinSkills()).data.data,
    staleTime: Infinity,
  })
}

export function useSkill(name: string) {
  return useQuery({
    queryKey: skillKey(name),
    queryFn: async () => (await getSkill({ path: { skill: name } })).data,
  })
}

export function useSkillFiles(name: string, revision: number | undefined) {
  return useQuery({
    queryKey: skillFilesKey(name, revision ?? 0),
    queryFn: async () =>
      (await listSkillFiles({ path: { skill: name, revision: revision ?? 0 } })).data.data,
    enabled: revision !== undefined,
    staleTime: Infinity,
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
    queryKey: ["skill-search", q],
    queryFn: async () => (await searchSkills({ query: { q } })).data,
    enabled: q.length >= 2,
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  })
}

// useSkillAudits is what skills.sh's auditors said of a result, read when the owner picks it.
export function useSkillAudits(source: string | undefined, slug: string | undefined) {
  return useQuery({
    queryKey: ["skill-audits", source, slug],
    queryFn: async () =>
      (await listSkillAudits({ query: { source: source ?? "", skill: slug ?? "" } })).data.data,
    enabled: !!source && !!slug,
    staleTime: Infinity,
  })
}
