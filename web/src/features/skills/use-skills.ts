import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { getSkill, listBuiltinSkills, listSkillFiles, listSkills } from "@/api/generated/sdk.gen"
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
