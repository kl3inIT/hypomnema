import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listConnectionActionsOptions,
  listConnectionActionsQueryKey,
  listGitHostsOptions,
  listGitHostsQueryKey,
  listIntegrationsOptions,
  listIntegrationsQueryKey,
  listReposOptions,
  listReposQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import type { GitHost } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

export function useHosts() {
  return useQuery({ ...listGitHostsOptions(), select: (page) => page.data })
}

// useRefreshHosts reads the git hosts again after one changes: every cached read of their list.
export function useRefreshHosts() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listGitHostsQueryKey()) })
}

// The services that sign in with an account (issue #103): Google is the first.
export function useServices() {
  return useQuery({ ...listIntegrationsOptions(), select: (page) => page.data })
}

// useRefreshServices reads the services again after one of their accounts changes.
export function useRefreshServices() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listIntegrationsQueryKey()) })
}

// useConnectionActions reads what agents may do with one account of a service.
export function useConnectionActions(integration: string, label: string) {
  return useQuery({
    ...listConnectionActionsOptions({ path: { integration, label } }),
    select: (page) => page.data,
  })
}

// useRefreshConnectionActions reads one account's actions again after one changes.
export function useRefreshConnectionActions() {
  const queryClient = useQueryClient()

  return (integration: string, label: string) =>
    queryClient.invalidateQueries({
      queryKey: listConnectionActionsQueryKey({ path: { integration, label } }),
    })
}

export function useRepos() {
  return useQuery({ ...listReposOptions(), select: (page) => page.data })
}

// useRefreshRepos reads the repositories again after one changes.
export function useRefreshRepos() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listReposQueryKey()) })
}

// hostName is a host as a person names it: the product for the two everyone knows, the address for a company's
// server.
export function hostName(host: GitHost): string {
  if (host.host === "github.com") return "GitHub"
  if (host.host === "gitlab.com") return "GitLab"

  return host.host
}

// kindName is the product a host runs.
export function kindName(host: GitHost): string {
  return host.kind === "github" ? "GitHub" : "GitLab"
}

// hostOf takes a host as it is typed or pasted: its name, or a URL copied from the browser.
export function hostOf(typed: string): string {
  const rest = typed.trim().replace(/^[a-z]+:\/\//i, "")

  return (rest.split("/")[0] ?? "").toLowerCase()
}

// nameOf proposes the name a repository is registered under: the last part of what was typed.
export function nameOf(repo: string): string {
  const parts = repo
    .trim()
    .replace(/\/+$/, "")
    .replace(/\.git$/i, "")
    .split("/")
    .filter(Boolean)

  return (parts.at(-1) ?? "").toLowerCase()
}

// tokenHelp says which token a host needs and where its maker explains how to create one.
export function tokenHelp(host: GitHost): { needs: string; docs: string } {
  return host.kind === "github"
    ? {
        needs:
          "A fine-grained personal access token for the repositories workers work in, with read and write access to Contents and Pull requests.",
        docs: "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens",
      }
    : {
        needs:
          "A personal, group or project access token with the scopes api and write_repository.",
        docs: "https://docs.gitlab.com/user/profile/personal_access_tokens/",
      }
}
