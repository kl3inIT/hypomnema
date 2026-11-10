import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronRight } from "lucide-react"
import { useState } from "react"

import type { Profile } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
import { RuntimeMark } from "@/features/board/runtime-mark"
import { ConfirmDialog } from "@/features/settings/settings-parts"
import { PageHeader } from "@/features/shell/app-shell"
import { since } from "@/lib/time"

import { StandingText } from "./agents-screen"
import { ProfileEditor } from "./profile-editor"
import { draftOf, emptyDraft, runtimeName } from "./profiles-state"
import { useApplyProfile, useModels, useProfile, useProfileVersions } from "./use-profiles"

type View = "configuration" | "versions"

// Crumbs is the way back to the list, as Omnara's "Agents › name".
function Crumbs({ here }: { here: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link to="/agents" className="text-muted-foreground hover:text-foreground">
        Agents
      </Link>
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden />
      <span className="truncate">{here}</span>
    </span>
  )
}

// ProfileScreen is one profile: its name, its version and whether a worker can start now, then its configuration
// or its versions. The header is Omnara's (AgentProfileView.tsx: the name, "Version N" under it); the editor is
// keyed by the version, so a save shows the stored profile again, as Omnara re-keys on the new config.
export function ProfileScreen({ name }: { name: string }) {
  const profile = useProfile(name)
  const models = useModels()
  const [view, setView] = useState<View>("configuration")
  const p = profile.data

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here={name} />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-2xl flex-col gap-5 pt-2">
          {profile.error ? (
            <div className="flex flex-col items-center gap-3 py-10 text-sm text-muted-foreground">
              <p>This agent could not be read. It may not exist.</p>
              <Link to="/agents" className={buttonVariants({ variant: "outline", size: "sm" })}>
                Back to Agents
              </Link>
            </div>
          ) : !p ? (
            <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
          ) : (
            <>
              <header className="flex items-start gap-3">
                <RuntimeMark runtime={p.runtime} className="mt-1 size-6 shrink-0" />
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <h2 className="truncate text-lg font-semibold tracking-tight">{p.name}</h2>
                  <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <span>
                      Version {p.version} · {runtimeName(p.runtime)}
                    </span>
                    <StandingText profile={p} />
                  </p>
                </div>
              </header>

              <fieldset className="m-0 inline-flex w-fit gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
                <legend className="sr-only">Show</legend>
                {(["configuration", "versions"] as const).map((v) => (
                  <button
                    key={v}
                    type="button"
                    aria-pressed={view === v}
                    onClick={() => setView(v)}
                    className="rounded-md px-2.5 py-1 text-xs text-muted-foreground capitalize aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
                  >
                    {v}
                  </button>
                ))}
              </fieldset>

              {/* The editor stays mounted while the versions show, so unsaved edits are kept (Omnara keeps its
                  Configuration tab mounted and hidden). */}
              <div hidden={view !== "configuration"}>
                <ProfileEditor
                  key={p.version_id}
                  start={draftOf(p)}
                  startSource={p.source}
                  models={models.data ?? []}
                  stored={p}
                  onSaved={() => undefined}
                />
              </div>
              {view === "versions" && <Versions profile={p} />}
            </>
          )}
        </div>
      </div>
    </main>
  )
}

// Versions lists a profile's versions, newest first, each opening to its source. It is OpenMausBot's history of a
// bot's changes (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/components/bot-settings/
// HistorySection.tsx): a row a change with its time, and a way back to an earlier one that adds a new entry and
// removes none. A profile's version is immutable, so restoring one applies its source again.
function Versions({ profile }: { profile: Profile }) {
  const versions = useProfileVersions(profile.name, true)
  const apply = useApplyProfile()

  if (versions.error) {
    return <p className="py-6 text-sm text-destructive">The versions could not be read.</p>
  }

  if (!versions.data) return <p className="py-6 text-sm text-muted-foreground">Loading…</p>

  return (
    <ol className="flex flex-col gap-2" aria-label="Versions">
      {versions.data.map((v) => {
        const current = v.version_id === profile.version_id

        return (
          <li key={v.version_id} className="rounded-lg border bg-card">
            <details className="group">
              <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 text-sm">
                <ChevronRight
                  className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90"
                  aria-hidden
                />
                <span className="font-medium">Version {v.version}</span>
                <span className="text-xs text-muted-foreground">· {since(v.created_at)}</span>
                {current && <span className="ms-auto text-xs text-muted-foreground">Current</span>}
              </summary>
              <div className="flex flex-col gap-3 border-t px-3 py-3">
                <pre className="overflow-x-auto rounded-md bg-muted px-3 py-2.5 font-mono text-xs leading-relaxed whitespace-pre-wrap">
                  {v.source}
                </pre>
                {!current && (
                  <div>
                    <ConfirmDialog
                      trigger={
                        <Button variant="outline" size="sm">
                          Restore this version
                        </Button>
                      }
                      title={`Restore version ${v.version}?`}
                      consequence={`Makes a new version of ${profile.name} with this configuration. Version ${profile.version} stays in the list.`}
                      action="Restore"
                      run={() => apply.mutateAsync({ name: profile.name, source: v.source })}
                      onDone={() => versions.refetch()}
                      done={`Version ${v.version} restored`}
                    />
                  </div>
                )}
              </div>
            </details>
          </li>
        )
      })}
    </ol>
  )
}

// NewAgentScreen makes a profile: the name, then the same editor. It is a page, as Omnara's "New agent"
// (CreateAgentFormView.tsx), and opens the new profile once the server stored it.
export function NewAgentScreen() {
  const models = useModels()
  const navigate = useNavigate()

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here="New agent" />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-2xl flex-col gap-5 pt-2">
          <header className="flex flex-col gap-0.5">
            <h2 className="text-lg font-semibold tracking-tight">New agent</h2>
            <p className="text-sm text-muted-foreground">
              A profile a worker runs with. The coordinator can give Tasks to it once it is saved.
            </p>
          </header>
          <ProfileEditor
            start={emptyDraft}
            models={models.data ?? []}
            onSaved={(p) => void navigate({ to: "/agents/$name", params: { name: p.name } })}
          />
        </div>
      </div>
    </main>
  )
}
