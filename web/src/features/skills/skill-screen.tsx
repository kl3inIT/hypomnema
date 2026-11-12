import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { ArrowLeft, Globe, Upload, X } from "lucide-react"
import type { ReactNode } from "react"

import "@/api/client"
import { deleteSkill, setSkillEveryone } from "@/api/generated/sdk.gen"
import type { Skill } from "@/api/generated/types.gen"
import { GitHubIcon } from "@/components/icons/brand-icons"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ConfirmDialog, ErrorAlert } from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { since } from "@/lib/time"

import { AddSkill } from "./add-skill"
import { SkillFileTree } from "./skill-file-tree"
import { EveryoneBadge, SourceIcon } from "./skill-parts"
import { githubAgain, size, sourceLine, usersOf, type Users } from "./skills-state"
import { skillsKey, useNamers, useSkill, useSkillFiles } from "./use-skills"

// SkillScreen is one skill's page (issue #21), after Omnara's (Apache-2.0, Copyright Omnara;
// frontend/apps/web/src/components/skills/SkillView.tsx): the name with a way back, "Upload new version" and the
// row's actions in the header, then the files of the newest revision as a tree. Added here: the switch that gives it
// to everyone, OpenMausBot's "Every bot" (src/components/bot-settings/SkillsSection.tsx); who uses it; "Update
// from GitHub", which fetches the same source again; and every revision, which the API already returns. Omnara
// edits SKILL.md in place; here a change is a new revision, from GitHub or an archive.
export function SkillScreen({ name }: { name: string }) {
  const skill = useSkill(name)

  if (skill.error) {
    return (
      <SettingsPage title={name}>
        <Back />
        <ErrorAlert error={skill.error} />
      </SettingsPage>
    )
  }

  if (!skill.data) {
    return (
      <SettingsPage title={name}>
        <Back />
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      </SettingsPage>
    )
  }

  return <Loaded skill={skill.data} />
}

function Back() {
  return (
    <Link
      to="/settings/skills"
      className="-mt-1 inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
    >
      <ArrowLeft className="size-4" />
      Skills
    </Link>
  )
}

function Loaded({ skill }: { skill: Skill }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const { profiles, bots } = useNamers()
  const users = usersOf(skill, profiles, bots)
  const files = useSkillFiles(skill.name, skill.latest.revision)
  const revisions = skill.revisions ?? [skill.latest]
  const again = githubAgain(skill.latest)

  const everyone = useMutation({
    mutationFn: async (next: boolean) =>
      setSkillEveryone({ path: { skill: skill.name }, body: { everyone: next } }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: skillsKey }),
  })

  return (
    <SettingsPage
      title={skill.name}
      wide
      actions={
        <>
          {again && (
            <AddSkill
              github={again}
              trigger={
                <Button variant="outline" size="sm">
                  <GitHubIcon data-icon="inline-start" />
                  Update from GitHub
                </Button>
              }
            />
          )}
          <AddSkill
            way="upload"
            trigger={
              <Button variant="outline" size="sm">
                <Upload data-icon="inline-start" />
                Upload new version
              </Button>
            }
          />
          <ConfirmDialog
            trigger={
              <Button variant="ghost" size="sm">
                Remove
              </Button>
            }
            title={`Remove ${skill.name}?`}
            consequence={`Its ${revisions.length === 1 ? "version goes" : `${revisions.length} versions go`} with it. Work already running keeps what it was given. This cannot be undone.`}
            action="Remove"
            run={() => deleteSkill({ path: { skill: skill.name } })}
            onDone={async () => {
              await navigate({ to: "/settings/skills" })
              await queryClient.invalidateQueries({ queryKey: skillsKey })
            }}
            done={`${skill.name} removed`}
          />
        </>
      }
    >
      <div className="flex items-center gap-2">
        <Back />
        {skill.everyone && <EveryoneBadge />}
      </div>
      <p className="text-sm text-muted-foreground">{skill.latest.description}</p>

      <div className="flex items-center gap-4 rounded-xl border bg-card px-4 py-3">
        <div className="min-w-0 flex-1">
          <label htmlFor="skill-everyone" className="text-sm font-medium">
            For every worker and bot
          </label>
          <p className="text-sm text-muted-foreground">
            Every agent and bot gets it, now and when one is added, unless it turns it off.
          </p>
        </div>
        <Switch
          id="skill-everyone"
          checked={skill.everyone}
          disabled={everyone.isPending}
          onCheckedChange={(next) => everyone.mutate(next)}
        />
      </div>
      <ErrorAlert error={everyone.error} />

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <Panel title="Used by">
          <UsedBy users={users} />
        </Panel>
        <Panel title="Files" count={skill.latest.files}>
          {files.error ? (
            <p className="text-sm text-destructive">The files could not be read.</p>
          ) : files.data ? (
            <SkillFileTree files={files.data} className="max-h-64" />
          ) : (
            <p className="text-sm text-muted-foreground">Loading…</p>
          )}
        </Panel>
      </div>

      <section aria-label="Versions" className="flex flex-col gap-3">
        <h2 className="text-sm font-medium">Versions</h2>
        <div className="relative max-w-full overflow-auto rounded-lg border">
          <table className="w-full text-sm">
            <thead className="border-b text-muted-foreground">
              <tr>
                {["#", "Added", "Source", "Commit", "Files", "Size", "Digest"].map((h) => (
                  <th
                    key={h}
                    scope="col"
                    className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {revisions.map((r) => (
                <tr key={r.revision} data-testid="revision" className="border-b last:border-b-0">
                  <td className="h-11 px-3">{r.revision}</td>
                  <td className="px-3 whitespace-nowrap">{since(r.created_at)}</td>
                  <td className="px-3 whitespace-nowrap">
                    <span className="flex items-center gap-2">
                      <SourceIcon source={r.source} className="size-3.5" />
                      {sourceLine(r)}
                    </span>
                  </td>
                  <td className="px-3 font-mono text-xs">
                    {r.commit ? (
                      r.commit.slice(0, 12)
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                  <td className="px-3">{r.files}</td>
                  <td className="px-3 whitespace-nowrap">{size(r.size)}</td>
                  <td className="px-3 font-mono text-xs text-muted-foreground" title={r.digest}>
                    {r.digest.slice(0, 12)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-xs text-muted-foreground">
          A worker keeps the version it was given when its work started; the next one gets the
          newest.
        </p>
      </section>
    </SettingsPage>
  )
}

function Panel({ title, count, children }: { title: string; count?: number; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-2 rounded-xl border p-4">
      <h2 className="text-sm font-medium">
        {title}
        {count !== undefined && <span className="ms-1.5 text-muted-foreground">{count}</span>}
      </h2>
      {children}
    </section>
  )
}

function Names({ names }: { names: string[] }) {
  return (
    <span className="flex min-w-0 flex-wrap gap-1">
      {names.map((n) => (
        <span key={n} className="rounded-md border bg-muted/40 px-1.5 text-xs leading-5">
          {n}
        </span>
      ))}
    </span>
  )
}

function UsedBy({ users }: { users: Users }) {
  if (users.everyone) {
    return (
      <div className="flex flex-col gap-2 text-sm">
        <p className="flex items-center gap-2">
          <Globe className="size-3.5 text-muted-foreground" aria-hidden />
          Everyone
        </p>
        {users.off.length > 0 && (
          <div className="flex items-start gap-2">
            <X className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
            <span className="shrink-0 text-muted-foreground">Turned off by</span>
            <Names names={users.off} />
          </div>
        )}
      </div>
    )
  }

  if (users.adding.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No one yet. Give it to everyone above, or add it to an agent or a bot.
      </p>
    )
  }

  return <Names names={users.adding} />
}
