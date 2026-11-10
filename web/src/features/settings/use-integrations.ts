import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { listGitHosts, listIntegrations, listRepos } from "@/api/generated/sdk.gen"
import type { GitHost } from "@/api/generated/types.gen"
import { toast } from "@/components/ui/toast"

export const hostsKey = ["git-hosts"] as const
export const reposKey = ["repos"] as const

export function useHosts() {
  return useQuery({ queryKey: hostsKey, queryFn: async () => (await listGitHosts()).data.data })
}

// The services that sign in with an account (issue #103): Google is the first.
export const servicesKey = ["integrations"] as const

export function useServices() {
  return useQuery({
    queryKey: servicesKey,
    queryFn: async () => (await listIntegrations()).data.data,
  })
}

export function useRepos() {
  return useQuery({ queryKey: reposKey, queryFn: async () => (await listRepos()).data.data })
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

export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}

// said tells the owner, in a toast, that a change he made was done. Omnigent, Orca, Infisical and Coder confirm a
// change the same way (sonner); this is the toast of the project's own component style.
export function said(what: string) {
  toast.add({ title: what, type: "success" })
}
