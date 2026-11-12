import { ExternalLink, Search } from "lucide-react"
import { useEffect, useState } from "react"

import type { SkillSearchResult } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"

import {
  isAdded,
  auditorName,
  auditResult,
  installsLabel,
  searchDelay,
  slugOf,
  type AuditTone,
} from "./skills-state"
import { useSkillAudits, useSkillSearch, useSkills } from "./use-skills"

// SkillsShSearch is the "skills.sh" tab of Add skill (issue #21, step 5): a search box and one row a result, as
// skills.sh's own search lists them (the skill's name, its repository, its installs; pictures in
// .tmp/ref-skillssh) and the skills CLI's search prompt behaves (src/find.ts: two characters before it asks, a
// shorter wait for a longer query, the last results kept while the next come). A picked result shows what
// skills.sh's auditors said of it, as the skill's page on skills.sh and the CLI's `skills add` show them before
// installing; a skill already here says so, as Orca's catalog card marks an installed plugin
// (components/plugins/list/PluginCard.tsx).
export function SkillsShSearch({
  picked,
  onPick,
}: {
  picked: SkillSearchResult | null
  onPick: (result: SkillSearchResult | null) => void
}) {
  const [typed, setTyped] = useState("")
  const [query, setQuery] = useState("")
  const search = useSkillSearch(query)
  const skills = useSkills().data ?? []

  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(typed), searchDelay(typed))

    return () => window.clearTimeout(timer)
  }, [typed])

  const results = search.data?.data ?? []
  const exact = search.data?.exact ?? results.length
  const short = typed.trim().length < 2

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <InputGroup>
        <InputGroupAddon>{search.isFetching ? <Spinner /> : <Search />}</InputGroupAddon>
        <InputGroupInput
          aria-label="Search skills.sh"
          placeholder="Search skills.sh"
          autoComplete="off"
          spellCheck={false}
          value={typed}
          onChange={(event) => {
            setTyped(event.target.value)
            onPick(null)
          }}
        />
      </InputGroup>

      {short ? (
        <p className="py-4 text-center text-sm text-muted-foreground">
          Type two characters or more to search the skills on skills.sh.
        </p>
      ) : search.error ? (
        <p role="alert" className="py-4 text-center text-sm text-destructive">
          skills.sh could not be searched.
        </p>
      ) : results.length === 0 ? (
        <p className="py-4 text-center text-sm text-muted-foreground">
          {search.isFetching || !search.data ? "Searching…" : "No skills found."}
        </p>
      ) : (
        <ul
          aria-label="Skills found"
          className="-mx-1 flex max-h-72 min-w-0 flex-col overflow-y-auto"
        >
          {results.map((r, i) => (
            <li key={r.id} className="min-w-0">
              {i === exact && (
                <p className="px-2 pt-3 pb-1 text-xs text-muted-foreground">
                  Matching some of the words
                </p>
              )}
              <button
                type="button"
                aria-pressed={picked?.id === r.id}
                onClick={() => onPick(picked?.id === r.id ? null : r)}
                className="flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-2 text-start text-sm hover:bg-muted/60 aria-pressed:bg-muted"
              >
                <span className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2">
                  <span className="font-medium">{r.name}</span>
                  <span className="truncate text-xs text-muted-foreground">{r.source}</span>
                </span>
                {isAdded(r, skills) && <Badge variant="secondary">Added</Badge>}
                <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                  {installsLabel(r.installs)}
                </span>
              </button>
              {picked?.id === r.id && <Picked result={r} />}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

const toneClass: Record<AuditTone, string> = {
  good: "text-done",
  warn: "text-needs-you",
  bad: "text-destructive",
  none: "text-muted-foreground",
}

// Picked is a result the owner chose: what skills.sh's auditors said of it, and its page there.
function Picked({ result }: { result: SkillSearchResult }) {
  const audits = useSkillAudits(result.source, slugOf(result.id))

  return (
    <div className="mx-2 mt-1 mb-2 flex flex-col gap-2 rounded-md border bg-card px-3 py-2.5 text-sm">
      <p className="text-xs font-medium text-muted-foreground">Security audits on skills.sh</p>
      {audits.isPending ? (
        <p className="text-xs text-muted-foreground">Reading…</p>
      ) : (audits.data ?? []).length === 0 ? (
        <p className="text-xs text-muted-foreground">skills.sh has no audits of this skill.</p>
      ) : (
        <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1">
          {(audits.data ?? []).map((a) => {
            const { text, tone } = auditResult(a)

            return (
              <div key={a.partner} className="contents">
                <dt>{auditorName(a.partner)}</dt>
                <dd className={cn("text-end", toneClass[tone])}>{text}</dd>
              </div>
            )
          })}
        </dl>
      )}
      <a
        href={`https://skills.sh/${result.id}`}
        target="_blank"
        rel="noreferrer"
        className="inline-flex w-fit items-center gap-1 text-xs text-link hover:underline"
      >
        Its page on skills.sh
        <ExternalLink className="size-3" aria-hidden />
      </a>
    </div>
  )
}
