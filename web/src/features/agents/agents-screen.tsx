import { Link } from "@tanstack/react-router"
import { useState } from "react"

import type { Profile } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
import { RuntimeMark } from "@/features/board/runtime-mark"
import { PageHeader } from "@/features/shell/app-shell"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { effortLabel, modelLine, runtimeName, standing, summary } from "./profiles-state"
import { useProfiles } from "./use-profiles"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.agents.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// AgentsScreen lists the profiles the coordinator chooses from (issue #142). The cards are Omnara's
// (Apache License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/agents/
// AgentProfilesSection.tsx and AgentCardList.tsx): the name as a link that covers the card, the model under it, and
// a foot strip with "Version N". Changed: a profile's description is on the card, since the coordinator chooses by
// it; the foot says whether a worker can start now, in Omnigent's coloured words (web/src/pages/settings/
// SettingsHarnessesSection.tsx), where Omnara has cost and instances; no search, sort or Launch. The Grid and
// Table switch is the Machines page's.
export function AgentsScreen() {
  const profiles = useProfiles()
  const [layout, setLayout] = useState<Layout>(readLayout)
  const list = profiles.data ?? []

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title="Agents">
        {list.length > 0 && (
          <span className="text-xs whitespace-nowrap text-muted-foreground">{summary(list)}</span>
        )}
        <div className="ms-auto flex items-center gap-2">
          <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
            <legend className="sr-only">Layout</legend>
            {(["grid", "table"] as const).map((l) => (
              <button
                key={l}
                type="button"
                aria-pressed={layout === l}
                onClick={() => choose(l)}
                className="rounded-md px-2.5 py-1 text-xs text-muted-foreground capitalize aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
              >
                {l}
              </button>
            ))}
          </fieldset>
          <Link to="/agents/new" className={buttonVariants()}>
            New agent
          </Link>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        <p className="pb-3 text-xs text-muted-foreground">
          The coordinator gives each Task to one of these, choosing by its description.
        </p>
        {profiles.error ? (
          <div className="flex flex-col items-center gap-3 py-10 text-sm text-muted-foreground">
            <p>Couldn&apos;t load this list.</p>
            <Button variant="outline" size="sm" onClick={() => void profiles.refetch()}>
              Retry
            </Button>
          </div>
        ) : profiles.isPending ? (
          <div
            className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
            aria-label="Loading"
          >
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-[7.25rem] animate-pulse rounded-xl bg-muted" />
            ))}
          </div>
        ) : list.length === 0 ? (
          <div className="mx-auto flex max-w-sm flex-col items-center gap-4 py-16 text-center">
            <p className="text-sm text-muted-foreground">
              No agents yet. An agent is a saved profile a worker runs with: its CLI, its model and
              its instructions.
            </p>
            <Link to="/agents/new" className={buttonVariants()}>
              New agent
            </Link>
          </div>
        ) : layout === "table" ? (
          <AgentsTable profiles={list} />
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {list.map((profile) => (
              <AgentCard key={profile.name} profile={profile} />
            ))}
          </div>
        )}
      </div>
    </main>
  )
}

// StandingText says whether a worker can start under the profile now; nothing for the coordinator's.
export function StandingText({ profile, className }: { profile: Profile; className?: string }) {
  const s = standing(profile)
  if (!s) return null

  return (
    <span
      data-tone={s.tone}
      className={cn(
        "inline-flex min-w-0 items-center gap-1.5 data-[tone=good]:text-done data-[tone=warn]:text-needs-you",
        className,
      )}
    >
      <span className="size-1.5 shrink-0 rounded-full bg-current" aria-hidden />
      <span className="truncate">{s.text}</span>
    </span>
  )
}

function AgentCard({ profile }: { profile: Profile }) {
  return (
    <article
      data-testid="agent"
      aria-label={profile.name}
      className="relative flex w-full flex-col overflow-hidden rounded-xl border bg-card text-card-foreground hover:border-foreground/25"
    >
      <div className="flex flex-1 items-start gap-3 px-4 pt-3.5 pb-3">
        <RuntimeMark runtime={profile.runtime} className="mt-0.5 size-5 shrink-0" />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h2 className="truncate text-sm font-medium">
            <Link
              to="/agents/$name"
              params={{ name: profile.name }}
              className="outline-none after:absolute after:inset-0 after:rounded-xl focus-visible:after:ring-2 focus-visible:after:ring-ring"
            >
              {profile.name}
            </Link>
          </h2>
          <p className="line-clamp-2 text-sm text-muted-foreground">
            {profile.description || "No description."}
          </p>
          <p className="truncate pt-0.5 text-xs text-muted-foreground">
            <span className="font-mono">{modelLine(profile)}</span>
            {profile.provider && <> · {profile.provider}</>}
            {profile.effort && <> · {effortLabel(profile.effort)}</>}
          </p>
        </div>
      </div>
      <footer className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/60 bg-muted/30 px-4 py-2 text-xs text-muted-foreground">
        <span className="whitespace-nowrap">
          Version {profile.version} · {since(profile.created_at)}
        </span>
        <StandingText profile={profile} className="ms-auto" />
      </footer>
    </article>
  )
}

function AgentsTable({ profiles }: { profiles: Profile[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            {["Name", "Now", "Runs on", "Model", "Effort", "Version"].map((name) => (
              <th
                key={name}
                scope="col"
                className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
              >
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {profiles.map((profile) => (
            <tr key={profile.name} data-testid="agent" className="border-b last:border-b-0">
              <td className="max-w-80 px-3 py-2.5">
                <Link
                  to="/agents/$name"
                  params={{ name: profile.name }}
                  className="font-medium hover:underline"
                >
                  {profile.name}
                </Link>
                <p className="truncate text-xs text-muted-foreground">
                  {profile.description || "No description."}
                </p>
              </td>
              <td className="px-3 py-2.5 text-xs whitespace-nowrap">
                <StandingText profile={profile} />
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap">
                <span className="flex items-center gap-2">
                  <RuntimeMark runtime={profile.runtime} className="size-4" />
                  {runtimeName(profile.runtime)}
                </span>
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap">
                <span className="font-mono text-xs">{modelLine(profile)}</span>
                {profile.provider && (
                  <span className="text-xs text-muted-foreground"> · {profile.provider}</span>
                )}
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap">
                {profile.effort ? effortLabel(profile.effort) : "Default"}
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap text-muted-foreground">
                {profile.version} · {since(profile.created_at)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
