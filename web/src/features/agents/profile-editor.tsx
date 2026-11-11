import { useBlocker } from "@tanstack/react-router"
import { Brain, Trash2 } from "lucide-react"
import { useRef, useState } from "react"

import type { Model, Profile } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  NativeSelect,
  NativeSelectOptGroup,
  NativeSelectOption,
} from "@/components/ui/native-select"
import { Textarea } from "@/components/ui/textarea"

import {
  efforts,
  errorText,
  maxFallbacks,
  modelKey,
  problem,
  runtimeName,
  runtimes,
  sameDraft,
  toYaml,
  type Draft,
} from "./profiles-state"
import { useApplyProfile, useProfiles } from "./use-profiles"

type Mode = "builder" | "yaml"

// ProfileEditor edits one profile, new or stored, and saves it as a version. It is Omnara's config editor
// (Apache License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/agents/
// AgentConfigEditor.tsx, AgentConfigBasicForm.tsx, AgentConfigModelField.tsx, AgentProfileConfigEditor.tsx,
// ConfirmDiscardYamlDialog.tsx): Builder and YAML as two pills, the effort joined to the model, "Save revision"
// off until something changed, "Discard changes", and a question before YAML edits are thrown away. The name that
// cannot change, the description and the runtime's select are Omnigent's form (Apache License 2.0,
// https://github.com/omnigent-ai/omnigent; web/src/pages/settings/CustomAgentConfigForm.tsx). Changed: YAML is a
// plain text field checked by the server, where Omnara has Monaco with a schema; the builder does not read YAML
// back, so going back to it always discards YAML edits; every model is listed, and the server says which a
// runtime cannot use.
export function ProfileEditor({
  start,
  startSource,
  models,
  stored,
  onSaved,
}: {
  start: Draft
  // startSource is the stored YAML of a profile that exists; a new one starts from the builder's.
  startSource?: string
  models: Model[]
  // stored is the profile being edited; absent for a new one, whose name can be typed.
  stored?: Profile
  onSaved: (profile: Profile) => void
}) {
  const [draft, setDraft] = useState(start)
  const [mode, setMode] = useState<Mode>("builder")
  // yaml is what the YAML field holds once it was edited by hand; null while the builder writes it.
  const [yaml, setYaml] = useState<string | null>(null)
  const [asking, setAsking] = useState(false)
  const apply = useApplyProfile()

  const built = toYaml(draft, models)
  const untouched = sameDraft(draft, start)
  const shown = yaml ?? (untouched && startSource ? startSource : built)
  const dirty = yaml !== null ? yaml !== (startSource ?? "") : !untouched
  const wrong = yaml === null ? problem(draft) : yaml.trim() === "" ? "The YAML is empty." : null

  // Omnara's guard (hooks/use-unsaved-changes-warning.ts): a confirm before unsaved changes are left behind.
  // saved is set as the server stores the profile, before the page moves on: the changes are no longer unsaved.
  const saved = useRef(false)

  useBlocker({
    shouldBlockFn: () =>
      !saved.current && !window.confirm("You have unsaved changes. Discard them?"),
    enableBeforeUnload: () => !saved.current,
    disabled: !dirty,
  })

  const set = (change: Partial<Draft>) => {
    apply.reset()
    saved.current = false
    setDraft((d) => ({ ...d, ...change }))
  }

  function show(next: Mode) {
    if (next === "builder" && yaml !== null && yaml !== built) {
      setAsking(true)

      return
    }

    if (next === "builder") setYaml(null)
    setMode(next)
  }

  function discard() {
    apply.reset()
    setDraft(start)
    setYaml(null)
  }

  function save() {
    const name = stored?.name ?? draft.name.trim()

    apply.mutate(
      { name, source: shown },
      {
        onSuccess: (profile) => {
          saved.current = true
          onSaved(profile)
        },
      },
    )
  }

  return (
    <form
      className="flex flex-col gap-5"
      onSubmit={(event) => {
        event.preventDefault()
        if (!wrong) save()
      }}
    >
      <div className="flex items-center justify-end">
        <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
          <legend className="sr-only">Edit as</legend>
          {(["builder", "yaml"] as const).map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={mode === m}
              onClick={() => show(m)}
              className="rounded-md px-2.5 py-1 text-xs text-muted-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-xs"
            >
              {m === "builder" ? "Builder" : "YAML"}
            </button>
          ))}
        </fieldset>
      </div>

      {mode === "builder" ? (
        <Builder draft={draft} set={set} models={models} renamable={!stored} />
      ) : (
        <Field>
          <FieldLabel htmlFor="profile-yaml">Config (YAML)</FieldLabel>
          <Textarea
            id="profile-yaml"
            spellCheck={false}
            autoComplete="off"
            code
            rows={18}
            value={shown}
            onChange={(event) => {
              apply.reset()
              setYaml(event.target.value)
            }}
          />
          <FieldDescription>
            The document the server stores. It checks it when you save and says what is wrong.
          </FieldDescription>
        </Field>
      )}

      {apply.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(apply.error)}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
        {wrong && dirty && <p className="me-auto text-xs text-muted-foreground">{wrong}</p>}
        {dirty && !apply.isPending && stored && (
          <Button type="button" variant="ghost" onClick={discard}>
            Discard changes
          </Button>
        )}
        <Button type="submit" disabled={apply.isPending || !dirty || wrong !== null}>
          {stored
            ? apply.isPending
              ? "Saving…"
              : "Save revision"
            : apply.isPending
              ? "Creating…"
              : "Create profile"}
        </Button>
      </div>

      <AlertDialog open={asking} onOpenChange={setAsking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard YAML edits?</AlertDialogTitle>
            <AlertDialogDescription>
              The builder does not read YAML back. Switching to it discards these edits and writes
              the config from the builder&apos;s fields again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing YAML</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                setYaml(null)
                setMode("builder")
                setAsking(false)
              }}
            >
              Discard edits
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </form>
  )
}

function Builder({
  draft,
  set,
  models,
  renamable,
}: {
  draft: Draft
  set: (change: Partial<Draft>) => void
  models: Model[]
  renamable: boolean
}) {
  const providers = [...new Set(models.map((m) => m.provider))].sort()
  const known =
    draft.model === "" || models.some((m) => modelKey(m.provider, m.name) === draft.model)

  return (
    <div className="flex flex-col gap-5">
      <Field>
        <FieldLabel htmlFor="profile-name">Name</FieldLabel>
        <Input
          id="profile-name"
          required
          readOnly={!renamable}
          autoComplete="off"
          spellCheck={false}
          placeholder="codex-implementer"
          value={draft.name}
          onChange={(event) => set({ name: event.target.value })}
        />
        <FieldDescription>
          {renamable
            ? "A Task names its agent by this. Letters, numbers, hyphens and underscores."
            : "The name cannot be changed."}
        </FieldDescription>
      </Field>

      <Field>
        <FieldLabel htmlFor="profile-description">Description</FieldLabel>
        <Textarea
          id="profile-description"
          required
          rows={2}
          placeholder="Makes a narrow, well-scoped change in the repository and opens a pull request."
          value={draft.description}
          onChange={(event) => set({ description: event.target.value })}
        />
        <FieldDescription>
          What this agent is for. The coordinator chooses an agent for each Task by this line.
        </FieldDescription>
      </Field>

      <Field>
        <FieldLabel htmlFor="profile-runtime">Runs on</FieldLabel>
        <NativeSelect
          id="profile-runtime"
          value={draft.runtime}
          onChange={(event) => set({ runtime: event.target.value })}
        >
          {runtimes.map((r) => (
            <NativeSelectOption key={r} value={r}>
              {runtimeName(r)}
              {r === "pi" ? " (the coordinator)" : ""}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <FieldDescription>The CLI a worker of this agent is, on a machine.</FieldDescription>
      </Field>

      <Field>
        <FieldLabel htmlFor="profile-model">Model</FieldLabel>
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect
            id="profile-model"
            value={draft.model}
            onChange={(event) => set({ model: event.target.value })}
          >
            <NativeSelectOption value="">Runtime default</NativeSelectOption>
            {!known && (
              <NativeSelectOption value={draft.model}>
                {draft.model} (not offered)
              </NativeSelectOption>
            )}
            {providers.map((provider) => (
              <NativeSelectOptGroup key={provider} label={provider}>
                {models
                  .filter((m) => m.provider === provider)
                  .map((m) => (
                    <NativeSelectOption
                      key={modelKey(m.provider, m.name)}
                      value={modelKey(m.provider, m.name)}
                    >
                      {m.name}
                    </NativeSelectOption>
                  ))}
              </NativeSelectOptGroup>
            ))}
          </NativeSelect>
          <span className="flex items-center gap-1.5">
            <Brain className="size-4 text-muted-foreground" aria-hidden />
            <NativeSelect
              aria-label="Effort"
              value={draft.effort}
              onChange={(event) => set({ effort: event.target.value })}
            >
              <NativeSelectOption value="">Default</NativeSelectOption>
              {efforts.map((e) => (
                <NativeSelectOption key={e.value} value={e.value}>
                  {e.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </span>
        </div>
        <FieldDescription>
          {known
            ? "Every provider's models are listed. Saving says so when this runtime cannot use the one chosen."
            : "No provider offers this model any more. Pick another, or the runtime's default."}
        </FieldDescription>
      </Field>

      <Field>
        <FieldLabel htmlFor="profile-instruction">Instructions</FieldLabel>
        <Textarea
          id="profile-instruction"
          rows={7}
          placeholder="Review the change the task names. Report what is wrong or missing, with file and line."
          value={draft.instruction}
          onChange={(event) => set({ instruction: event.target.value })}
        />
        <FieldDescription>
          Added after the rules every worker gets, which it may not override. Optional.
        </FieldDescription>
      </Field>

      {draft.runtime !== "pi" && (
        <Fallbacks
          own={draft.name}
          chain={draft.fallbacks}
          onChange={(fallbacks) => set({ fallbacks })}
        />
      )}
    </div>
  )
}

// Fallbacks is the ordered list of agents that take this one's Tasks while it is out of quota (issue #171), after
// OpenMausBot's backups of a bot (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/bot-panel/BackupAis.tsx): numbered rows, each changed or removed in place, then one control that
// adds the next. Changed: a row is a native select of the other agents, where OpenMausBot opens its model menu.
function Fallbacks({
  own,
  chain,
  onChange,
}: {
  own: string
  chain: string[]
  onChange: (chain: string[]) => void
}) {
  // The agents a Task can run under: every worker's but this one. The coordinator's has no answer to "can run".
  const workers = (useProfiles().data ?? [])
    .filter((p) => p.available !== undefined && p.name !== own)
    .map((p) => p.name)
  const free = workers.filter((name) => !chain.includes(name))

  return (
    <Field>
      <FieldLabel htmlFor="profile-fallback-add">Fallbacks</FieldLabel>
      {chain.length > 0 && (
        <ol aria-label="Fallbacks, in order" className="flex flex-col gap-1.5">
          {chain.map((name, index) => (
            <li key={name} className="flex items-center gap-2">
              <span className="w-3 text-xs text-muted-foreground tabular-nums" aria-hidden>
                {index + 1}
              </span>
              <NativeSelect
                aria-label={`Fallback ${index + 1}`}
                value={name}
                onChange={(event) =>
                  onChange(chain.map((n, at) => (at === index ? event.target.value : n)))
                }
              >
                <NativeSelectOption value={name}>{name}</NativeSelectOption>
                {free.map((n) => (
                  <NativeSelectOption key={n} value={n}>
                    {n}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Remove fallback ${name}`}
                onClick={() => onChange(chain.filter((_, at) => at !== index))}
              >
                <Trash2 aria-hidden />
              </Button>
            </li>
          ))}
        </ol>
      )}
      {chain.length < maxFallbacks && (
        <NativeSelect
          id="profile-fallback-add"
          value=""
          disabled={free.length === 0}
          onChange={(event) => {
            if (event.target.value) onChange([...chain, event.target.value])
          }}
        >
          <NativeSelectOption value="">Add a fallback…</NativeSelectOption>
          {free.map((n) => (
            <NativeSelectOption key={n} value={n}>
              {n}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      )}
      <FieldDescription>
        While this agent&apos;s accounts wait for a limit to reset, a Task that has not started runs
        under the first of these that can run. None: the Task waits for the reset.
      </FieldDescription>
    </Field>
  )
}
