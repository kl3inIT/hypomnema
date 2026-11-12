import { Link } from "@tanstack/react-router"
import { Box, Brain, CircleAlert, CircleCheck, History, LifeBuoy, Search } from "lucide-react"
import { useState, type ReactNode } from "react"

import type { Profile } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { RuntimeMark } from "@/features/board/runtime-mark"
import { isVisible, useColumnVisibility } from "@/features/machines/column-visibility"
import { ColumnsMenu } from "@/features/machines/columns-menu"
import { PageHeader } from "@/features/shell/app-shell"
import { cn } from "@/lib/utils"

import { AgentAvatar } from "./agent-avatar"
import { ModelMark } from "./model-mark"
import { effortLabel, modelLine, runtimeName, savedAgo, standing, summary } from "./profiles-state"
import { useProfiles } from "./use-profiles"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.agents.layout"
const columnsKey = "hypomnema.agents.columns"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// AgentsScreen lists the profiles the coordinator chooses from (issue #142), as cards or a table with the Machines
// page's switch. Whether a worker can start under a profile now is said in words, as Omnigent says a harness's
// state (Apache License 2.0, https://github.com/omnigent-ai/omnigent; web/src/pages/settings/
// SettingsHarnessesSection.tsx). The list, its loading, error and empty states follow Omnara's
// (Apache License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/agents/
// AgentProfilesSection.tsx, AgentCardList.tsx); the card itself is the Machines page's (owner, 2026-11-11).
export function AgentsScreen() {
  // The search over names and descriptions, and "No results." when it finds none, are Omnara's toolbar
  // (components/data-table/ResourceListToolbar.tsx) and Omnigent's list (SettingsCustomAgentsSection.tsx).
  const profiles = useProfiles()
  const [layout, setLayout] = useState<Layout>(readLayout)
  const [query, setQuery] = useState("")
  const [visibility, show] = useColumnVisibility(columnsKey, hiddenColumns)
  const all = profiles.data ?? []
  const words = query.trim().toLowerCase()
  const list = words
    ? all.filter((p) => `${p.name} ${p.description}`.toLowerCase().includes(words))
    : all

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
        {all.length > 0 && (
          <span className="text-xs whitespace-nowrap text-muted-foreground">{summary(all)}</span>
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
          {layout === "table" && all.length > 0 && (
            <ColumnsMenu
              columns={tableColumns.filter((column) => column.enableHiding !== false)}
              visibility={visibility}
              onShow={show}
            />
          )}
          <Link to="/agents/new" className={buttonVariants()}>
            New agent
          </Link>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pb-3">
          <p className="min-w-0 flex-1 basis-64 text-xs text-muted-foreground">
            The coordinator gives each Task to one of these, choosing by its description.
          </p>
          {all.length > 0 && (
            <div className="w-full sm:w-64">
              <InputGroup>
                <InputGroupAddon>
                  <Search />
                </InputGroupAddon>
                <InputGroupInput
                  type="search"
                  autoComplete="off"
                  aria-label="Search agents"
                  placeholder="Search agents by name…"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </InputGroup>
            </div>
          )}
        </div>
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
              <div key={i} className="h-56 animate-pulse rounded-xl bg-muted" />
            ))}
          </div>
        ) : all.length > 0 && list.length === 0 ? (
          <p className="py-16 text-center text-sm text-muted-foreground">No results.</p>
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
          <AgentsTable
            profiles={list}
            columns={tableColumns.filter((column) => isVisible(visibility, column.id))}
          />
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

// StandingText says whether a worker can start under the profile now; nothing for a profile with no answer.
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

// The dot's colour for a profile's standing, as the Machines page's status dot; a profile with no standing has a
// quiet one.
function StandingDot({ profile }: { profile: Profile }) {
  const s = standing(profile)

  return (
    <span
      aria-hidden
      className={cn(
        "size-1.5 shrink-0 rounded-full",
        s === null ? "bg-muted-foreground/40" : s.tone === "good" ? "bg-done" : "bg-needs-you",
      )}
    />
  )
}

// Face is an agent's avatar with the mark of its CLI at the corner, as OpenMausBot badges a bot's avatar with its
// provider (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/components/BotModelBadge.tsx).
function Face({ profile }: { profile: Profile }) {
  return (
    <span className="relative shrink-0">
      <AgentAvatar name={profile.name} className="size-8" />
      <span className="absolute -end-1 -bottom-0.5 flex size-4 items-center justify-center rounded-full bg-card ring-1 ring-border">
        <RuntimeMark runtime={profile.runtime} className="size-2.5" />
      </span>
    </span>
  )
}

// AgentCard is the Machines page's card (machines-screen.tsx, after Beszel's SystemCard): a header with the status
// dot, the name and the state in words, then the description and one row a fact, each with its icon and its name.
// The name is a link that covers the card, as Omnara's.
function AgentCard({ profile }: { profile: Profile }) {
  const s = standing(profile)
  const rows = [
    ...(s
      ? [
          {
            name: "Now",
            icon:
              s.tone === "good" ? (
                <CircleCheck className="size-4 text-done" aria-hidden />
              ) : (
                <CircleAlert className="size-4 text-needs-you" aria-hidden />
              ),
            value: (
              <span className={s.tone === "good" ? undefined : "text-needs-you"}>{s.text}</span>
            ),
          },
        ]
      : []),
    {
      name: "Runs on",
      icon: <RuntimeMark runtime={profile.runtime} className="size-4" />,
      value: runtimeName(profile.runtime),
    },
    {
      name: "Model",
      icon: profile.model ? (
        <ModelMark name={profile.model} className="size-4" />
      ) : (
        <Box className="size-4 text-muted-foreground" aria-hidden />
      ),
      value: (
        <>
          <span className="shrink-0 font-mono text-xs">{modelLine(profile)}</span>
          {profile.provider && (
            <span className="ms-1.5 truncate text-xs text-muted-foreground">
              ({profile.provider})
            </span>
          )}
        </>
      ),
    },
    {
      name: "Effort",
      icon: <Brain className="size-4 text-muted-foreground" aria-hidden />,
      value: profile.effort ? effortLabel(profile.effort) : "Default",
    },
    ...(profile.fallbacks.length > 0
      ? [
          {
            name: "Fallback",
            icon: <LifeBuoy className="size-4 text-muted-foreground" aria-hidden />,
            value: <span className="truncate">{profile.fallbacks.join(", then ")}</span>,
          },
        ]
      : []),
    {
      name: "Version",
      icon: <History className="size-4 text-muted-foreground" aria-hidden />,
      value: (
        <>
          {profile.version}
          {savedAgo(profile) && (
            <span className="ms-1.5 text-xs text-muted-foreground">saved {savedAgo(profile)}</span>
          )}
        </>
      ),
    },
  ]

  return (
    <article
      data-testid="agent"
      aria-label={profile.name}
      className="relative flex w-full flex-col overflow-hidden rounded-xl border bg-card text-card-foreground hover:border-foreground/25"
    >
      <header className="flex items-center gap-2 border-b border-border/60 bg-muted/30 px-4 py-2">
        <h2 className="flex min-w-0 flex-1 items-center gap-2.5 text-sm font-semibold">
          <Face profile={profile} />
          <Link
            to="/agents/$name"
            params={{ name: profile.name }}
            className="truncate outline-none after:absolute after:inset-0 after:rounded-xl focus-visible:after:ring-2 focus-visible:after:ring-ring"
          >
            {profile.name}
          </Link>
        </h2>
        {s && (
          <span className="flex items-center gap-1.5 text-xs font-normal whitespace-nowrap text-muted-foreground">
            <StandingDot profile={profile} />
            {s.short}
          </span>
        )}
      </header>
      <p className="line-clamp-3 px-5 pt-3.5 text-sm text-foreground/85">
        {profile.description || "No description."}
      </p>
      <dl
        className="grid gap-2.5 px-5 pt-3.5 pb-4 text-sm"
        style={{ gridTemplateColumns: "24px minmax(64px, max-content) minmax(0, 1fr)" }}
      >
        {rows.map((row) => (
          <div key={row.name} className="col-span-3 grid grid-cols-subgrid items-center">
            <span className="flex items-center">{row.icon}</span>
            <dt className="pe-3 text-muted-foreground">{row.name}:</dt>
            <dd className="flex min-w-0 items-baseline">{row.value}</dd>
          </div>
        ))}
      </dl>
    </article>
  )
}

type TableColumn = {
  id: string
  name: string
  cell: (profile: Profile) => ReactNode
  className?: string
  enableHiding?: false
}

// hiddenColumns are hidden in the table until the owner shows them in its View menu: with them the table is wider
// than a 1280 px screen beside the sidebar (issue #242). The version and when it was saved matter when editing an
// agent, which its page shows, not when choosing one.
const hiddenColumns = ["version"] as const

// tableColumns are the table's columns, one list that the View menu and the table read, as the Machines page's
// (machines-columns.tsx, after Beszel's systems-table-columns.tsx).
const tableColumns: TableColumn[] = [
  {
    id: "name",
    name: "Name",
    enableHiding: false,
    className: "max-w-64",
    cell: (profile) => (
      <span className="flex min-w-0 items-center gap-2.5">
        <Face profile={profile} />
        <span className="min-w-0">
          <Link
            to="/agents/$name"
            params={{ name: profile.name }}
            className="font-medium hover:underline"
          >
            {profile.name}
          </Link>
          <span className="block truncate text-xs text-muted-foreground">
            {profile.description || "No description."}
          </span>
        </span>
      </span>
    ),
  },
  {
    id: "now",
    name: "Now",
    className: "max-w-56 text-xs whitespace-nowrap",
    cell: (profile) => (
      <span className="flex min-w-0" title={standing(profile)?.text}>
        <StandingText profile={profile} />
      </span>
    ),
  },
  {
    id: "runtime",
    name: "Runs on",
    className: "whitespace-nowrap",
    cell: (profile) => (
      <span className="flex items-center gap-2">
        <RuntimeMark runtime={profile.runtime} className="size-4" />
        {runtimeName(profile.runtime)}
      </span>
    ),
  },
  {
    id: "model",
    name: "Model",
    className: "whitespace-nowrap",
    cell: (profile) => (
      <>
        <span className="font-mono text-xs">{modelLine(profile)}</span>
        {profile.provider && (
          <span className="ms-1.5 text-xs text-muted-foreground">({profile.provider})</span>
        )}
      </>
    ),
  },
  {
    id: "effort",
    name: "Effort",
    className: "whitespace-nowrap",
    cell: (profile) => (profile.effort ? effortLabel(profile.effort) : "Default"),
  },
  {
    id: "version",
    name: "Version",
    className: "whitespace-nowrap text-muted-foreground",
    cell: (profile) => [profile.version, savedAgo(profile)].filter(Boolean).join(", "),
  },
]

function AgentsTable({ profiles, columns }: { profiles: Profile[]; columns: TableColumn[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            {columns.map((column) => (
              <th
                key={column.id}
                scope="col"
                className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
              >
                {column.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {profiles.map((profile) => (
            <tr key={profile.name} data-testid="agent" className="border-b last:border-b-0">
              {columns.map((column) => (
                <td key={column.id} className={cn("px-3 py-2.5", column.className)}>
                  {column.cell(profile)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
