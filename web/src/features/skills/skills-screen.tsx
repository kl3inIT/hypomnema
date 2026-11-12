import { Link } from "@tanstack/react-router"
import { Box, History, Plus, Search, Users as UsersIcon } from "lucide-react"
import { useState, type ReactNode } from "react"

import type { BuiltinSkill, Skill } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { SettingsPage } from "@/features/settings/settings-shell"
import { since } from "@/lib/time"

import { AddSkill } from "./add-skill"
import { EveryoneBadge, SkillMark, SourceIcon } from "./skill-parts"
import { builtinUsers, matches, sourceLine, usersLine, usersOf, type Users } from "./skills-state"
import { useBuiltinSkills, useNamers, useSkills } from "./use-skills"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.skills.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// SkillsScreen lists the skills the owner added and those the binary ships (issue #21), as the Machines page lists
// the nodes (machines-screen.tsx, after Beszel's systems-table): a grid of cards or a table, chosen with the same
// segmented control and kept in this browser. Skills are grouped by who owns them, as Omnara's page puts the
// user's and the organization's under two tabs (frontend/apps/web/src/routes/SkillsPage.tsx); here the owner's and
// those the binary ships, as two sections of one page (prototype the owner accepted, 2026-11-12). One search box by
// name, as Omnara's SkillsSection (components/overview/SkillsSection.tsx), also over the description. A card or row
// opens the skill's page; a built-in skill has none, since nothing about it is set.
export function SkillsScreen() {
  const skills = useSkills()
  const builtin = useBuiltinSkills()
  const { profiles, bots } = useNamers()
  const [layout, setLayout] = useState<Layout>(readLayout)
  const [query, setQuery] = useState("")

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  const added = (skills.data ?? [])
    .filter((s) => matches(query, s.name, s.latest.description))
    .map((skill) => ({ skill, users: usersOf(skill, profiles, bots) }))
  const shipped = (builtin.data ?? []).filter((s) => matches(query, s.name, s.description))

  return (
    <SettingsPage
      title="Skills"
      wide
      description="Instructions and scripts an agent's CLI reads when its work needs them. A skill is for no one until you give it to everyone, or add it to an agent or a bot."
      actions={
        <>
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
          <AddSkill
            trigger={
              <Button size="sm">
                <Plus data-icon="inline-start" />
                Add skill
              </Button>
            }
          />
        </>
      }
    >
      <div className="sm:max-w-xs">
        <InputGroup>
          <InputGroupAddon>
            <Search />
          </InputGroupAddon>
          <InputGroupInput
            aria-label="Search skills"
            placeholder="Search skills"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </InputGroup>
      </div>

      <Section title="Added" count={skills.data ? added.length : undefined}>
        {skills.error ? (
          <p className="py-6 text-center text-sm text-destructive">The skills could not be read.</p>
        ) : skills.isPending ? (
          <p className="py-6 text-center text-sm text-muted-foreground">Loading…</p>
        ) : added.length === 0 ? (
          <Empty searching={query.trim() !== ""} />
        ) : layout === "table" ? (
          <AddedTable rows={added} />
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {added.map(({ skill, users }) => (
              <AddedCard key={skill.name} skill={skill} users={users} />
            ))}
          </div>
        )}
      </Section>

      {shipped.length > 0 && (
        <Section title="Built in" count={shipped.length}>
          {layout === "table" ? (
            <BuiltinTable skills={shipped} />
          ) : (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {shipped.map((skill) => (
                <BuiltinCard key={skill.name} skill={skill} />
              ))}
            </div>
          )}
        </Section>
      )}
    </SettingsPage>
  )
}

function Section({
  title,
  count,
  children,
}: {
  title: string
  count: number | undefined
  children: ReactNode
}) {
  return (
    <section aria-label={title} className="flex flex-col gap-3">
      <h2 className="text-sm font-medium">
        {title}
        {count !== undefined && <span className="ms-1.5 text-muted-foreground">{count}</span>}
      </h2>
      {children}
    </section>
  )
}

// Card is the Machines page's card: a header with the mark, the name and its badge, then the description and one
// row a fact, with its icon, its name and its value.
function Card({
  header,
  description,
  children,
}: {
  header: ReactNode
  description: string
  children: ReactNode
}) {
  return (
    <>
      <header className="flex items-center gap-2.5 border-b border-border/60 bg-muted/30 px-4 py-2">
        {header}
      </header>
      <p className="line-clamp-2 px-4 pt-3 text-sm text-muted-foreground">{description}</p>
      <dl
        className="grid gap-2 px-4 pt-3 pb-4 text-sm"
        style={{ gridTemplateColumns: "20px minmax(72px, max-content) minmax(0, 1fr)" }}
      >
        {children}
      </dl>
    </>
  )
}

function Fact({ icon, name, children }: { icon: ReactNode; name: string; children: ReactNode }) {
  return (
    <div className="col-span-3 grid grid-cols-subgrid items-center">
      <span className="flex items-center text-muted-foreground [&>svg]:size-3.5">{icon}</span>
      <dt className="pe-3 text-muted-foreground">{name}</dt>
      <dd className="min-w-0 truncate">{children}</dd>
    </div>
  )
}

const cardClass =
  "w-full overflow-hidden rounded-xl border bg-card text-card-foreground transition-colors hover:border-foreground/20"

function AddedCard({ skill, users }: { skill: Skill; users: Users }) {
  return (
    <Link
      to="/settings/skills/$name"
      params={{ name: skill.name }}
      data-testid="skill"
      aria-label={skill.name}
      className={cardClass}
    >
      <Card
        description={skill.latest.description}
        header={
          <>
            <SkillMark name={skill.name} />
            <h3 className="min-w-0 flex-1 truncate font-mono text-sm font-semibold">
              {skill.name}
            </h3>
            {skill.everyone && <EveryoneBadge />}
          </>
        }
      >
        <Fact icon={<SourceIcon source={skill.latest.source} />} name="Source">
          <span className={skill.latest.source.startsWith("github:") ? "font-mono text-xs" : ""}>
            {sourceLine(skill.latest)}
          </span>
        </Fact>
        <Fact icon={<History />} name="Revision">
          {skill.latest.revision} · {since(skill.latest.created_at)}
        </Fact>
        <Fact icon={<UsersIcon />} name="Used by">
          {usersLine(users)}
        </Fact>
      </Card>
    </Link>
  )
}

function BuiltinCard({ skill }: { skill: BuiltinSkill }) {
  return (
    <article data-testid="built-in-skill" aria-label={skill.name} className={cardClass}>
      <Card
        description={skill.description}
        header={
          <>
            <SkillMark name={skill.name} builtin />
            <h3 className="min-w-0 flex-1 truncate font-mono text-sm font-semibold">
              {skill.name}
            </h3>
            <Badge variant="secondary">Built in</Badge>
          </>
        }
      >
        <Fact icon={<Box />} name="Comes with">
          Hypomnema
        </Fact>
        <Fact icon={<UsersIcon />} name="Used by">
          {builtinUsers(skill.name)}
        </Fact>
      </Card>
    </article>
  )
}

const thClass = "h-10 px-3 text-start text-xs font-medium whitespace-nowrap"

// AddedTable is the Machines page's table: it scrolls inside its box.
function AddedTable({ rows }: { rows: { skill: Skill; users: Users }[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            <th scope="col" className={thClass}>
              Skill
            </th>
            <th scope="col" className={thClass}>
              Source
            </th>
            <th scope="col" className={thClass}>
              Revision
            </th>
            <th scope="col" className={thClass}>
              Used by
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ skill, users }) => (
            <tr key={skill.name} data-testid="skill" className="border-b last:border-b-0">
              <td className="h-12 px-3">
                <Link
                  to="/settings/skills/$name"
                  params={{ name: skill.name }}
                  className="flex items-center gap-2.5 font-mono font-medium hover:underline"
                >
                  <SkillMark name={skill.name} />
                  {skill.name}
                  {skill.everyone && <EveryoneBadge />}
                </Link>
              </td>
              <td className="px-3 whitespace-nowrap">
                <span className="flex items-center gap-2">
                  <SourceIcon source={skill.latest.source} className="size-3.5" />
                  {sourceLine(skill.latest)}
                </span>
              </td>
              <td className="px-3 whitespace-nowrap">
                {skill.latest.revision} · {since(skill.latest.created_at)}
              </td>
              <td className="px-3">{usersLine(users)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function BuiltinTable({ skills }: { skills: BuiltinSkill[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            <th scope="col" className={thClass}>
              Skill
            </th>
            <th scope="col" className={thClass}>
              Description
            </th>
            <th scope="col" className={thClass}>
              Used by
            </th>
          </tr>
        </thead>
        <tbody>
          {skills.map((skill) => (
            <tr key={skill.name} data-testid="built-in-skill" className="border-b last:border-b-0">
              <td className="h-12 px-3">
                <span className="flex items-center gap-2.5 font-mono font-medium whitespace-nowrap">
                  <SkillMark name={skill.name} builtin />
                  {skill.name}
                </span>
              </td>
              <td className="max-w-md px-3 text-muted-foreground">
                <span className="line-clamp-1">{skill.description}</span>
              </td>
              <td className="px-3 whitespace-nowrap">{builtinUsers(skill.name)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Empty({ searching }: { searching: boolean }) {
  if (searching) {
    return <p className="py-6 text-center text-sm text-muted-foreground">No skill answers that.</p>
  }

  return (
    <div className="mx-auto flex max-w-sm flex-col items-center gap-4 py-10 text-center">
      <p className="text-sm text-muted-foreground">
        No skills added yet. Add one from a public GitHub repository, or upload its folder as an
        archive.
      </p>
      <AddSkill
        trigger={
          <Button variant="outline" size="sm">
            <Plus data-icon="inline-start" />
            Add skill
          </Button>
        }
      />
    </div>
  )
}
