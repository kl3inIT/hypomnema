import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronRight, Copy } from "lucide-react"
import { useState } from "react"

import type { Model, Profile } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
import { ConfirmDialog } from "@/features/settings/settings-parts"
import { MemoryEditor } from "@/features/memory/memory-editor"
import { useAgentMemory, useSetAgentMemory } from "@/features/memory/use-memory"
import { PageHeader } from "@/features/shell/app-shell"
import { AgentAvatar } from "./agent-avatar"
import { StandingText } from "./agents-screen"
import { ProfileEditor } from "./profile-editor"
import { copyName, draftOf, emptyDraft, runtimeName, savedAgo, type Draft } from "./profiles-state"
import { RuntimeMark } from "./runtime-mark"
import {
  useApplyProfile,
  useModels,
  useProfile,
  useProfiles,
  useProfileVersions,
} from "./use-profiles"

type View = "configuration" | "versions" | "memory"

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
              <header className="flex items-center gap-3.5">
                <AgentAvatar name={p.name} className="size-12 shrink-0" />
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <h2 className="truncate text-lg font-semibold tracking-tight">{p.name}</h2>
                  <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <span className="inline-flex items-center gap-1.5">
                      <RuntimeMark runtime={p.runtime} className="size-3.5" />
                      Version {p.version} · {runtimeName(p.runtime)}
                    </span>
                    <StandingText profile={p} />
                  </p>
                </div>
                {/* OpenMausBot's "Duplicate" in a bot's menu (Apache License 2.0,
                    https://github.com/milind-soni/OpenMausBot; src/components/SidebarBotMenu.tsx). */}
                <Link
                  to="/agents/new"
                  search={{ from: p.name }}
                  className={buttonVariants({ variant: "outline", size: "sm" })}
                >
                  <Copy data-icon="inline-start" />
                  Duplicate
                </Link>
              </header>

              <fieldset className="m-0 inline-flex w-fit gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
                <legend className="sr-only">Show</legend>
                {(["configuration", "versions", "memory"] as const).map((v) => (
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
              {view === "memory" && <AgentMemory name={p.name} />}
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
                {savedAgo(v) && (
                  <span className="text-xs text-muted-foreground">· {savedAgo(v)}</span>
                )}
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

// AgentMemory is what the agent's workers keep across their Tasks, shared by all of them on every machine (issue
// #21). It is not part of the agent's versions, as OpenMausBot's Memory is a section of a bot's settings beside its
// configuration, not part of it. A worker writes it with its memory tool; the owner edits it by hand here.
function AgentMemory({ name }: { name: string }) {
  const memory = useAgentMemory(name)
  const save = useSetAgentMemory(name)

  if (memory.error) {
    return <p className="py-6 text-sm text-destructive">The memory could not be read.</p>
  }

  if (!memory.data) return <p className="py-6 text-sm text-muted-foreground">Loading…</p>

  return (
    <MemoryEditor
      id="agent-memory"
      label="Memory"
      memory={memory.data.memory}
      limit={memory.data.memory_limit}
      writer="A worker"
      help="What this agent's workers keep across their tasks, one entry a line. Every worker of the agent gets it in its first message; they write it themselves, and you can edit it. A change is not a new version."
      save={{
        mutate: (text, { onSuccess }) =>
          save.mutate(text, { onSuccess: (m) => onSuccess(m.memory) }),
        isPending: save.isPending,
        error: save.error,
        reset: save.reset,
      }}
    />
  )
}

// NewAgentForm keeps the draft it started from. The list of agents is read again once the copy is stored, and a
// start worked out again from it (the next free name) would replace the form while it is saving.
function NewAgentForm({
  initial,
  models,
  onSaved,
}: {
  initial: Draft
  models: Model[]
  onSaved: (profile: Profile) => void
}) {
  const [start] = useState(initial)

  return <ProfileEditor start={start} models={models} onSaved={onSaved} />
}

// NewAgentScreen makes a profile: the name, then the same editor. It is a page, as Omnara's "New agent"
// (CreateAgentFormView.tsx), and opens the new profile once the server stored it.
//
// With from it starts as a copy of that agent (issue #200): the form filled in and nothing stored until
// "Create profile", as Hermes's create form clones a profile (MIT License,
// https://github.com/NousResearch/hermes-agent; web/src/pages/ProfilesPage.tsx, cloneFrom). What the copy takes,
// everything but the name, and the name's "copy" are OpenMausBot's (src/lib/bot-duplicate.ts,
// duplicateBotProfile).
export function NewAgentScreen({ from }: { from?: string }) {
  const models = useModels()
  const navigate = useNavigate()
  const source = useProfile(from ?? "", Boolean(from))
  const all = useProfiles()
  const waiting = Boolean(from) && !source.error && (!source.data || !all.data)
  const start =
    from && source.data
      ? {
          ...draftOf(source.data),
          name: copyName(
            source.data.name,
            (all.data ?? []).map((p) => p.name),
          ),
        }
      : emptyDraft

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here="New agent" />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-2xl flex-col gap-5 pt-2">
          <header className="flex flex-col gap-0.5">
            <h2 className="text-lg font-semibold tracking-tight">New agent</h2>
            <p className="text-sm text-muted-foreground">
              {from && source.data
                ? `A copy of ${from}. Change what differs, then create it.`
                : from && source.error
                  ? `${from} could not be read, so this starts empty.`
                  : "A profile a worker runs with. The coordinator can give Tasks to it once it is saved."}
            </p>
          </header>
          {waiting ? (
            <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
          ) : (
            <NewAgentForm
              initial={start}
              models={models.data ?? []}
              onSaved={(p) => void navigate({ to: "/agents/$name", params: { name: p.name } })}
            />
          )}
        </div>
      </div>
    </main>
  )
}
