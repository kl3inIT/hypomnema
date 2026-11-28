import { Brain } from "lucide-react"
import { lazy, Suspense, useState } from "react"

import type { Model, Profile, ToolAccess } from "@/api/generated/types.gen"
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
import { Spinner } from "@/components/ui/spinner"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { SkillsField } from "@/features/skills/skills-field"
import { ToolsField } from "@/features/tools/tools-field"
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

import { FallbackModels } from "./fallback-models"
import { ModelPicker } from "./model-picker"
import { errorText } from "@/lib/server-error"
import {
  efforts,
  emptyDraft,
  routingRoles,
  modelKey,
  problem,
  runtimeName,
  runtimes,
  sameDraft,
  toYaml,
  type Draft,
} from "./profiles-state"
import { RuntimeMark } from "@/components/icons/runtime-mark"
import { useApplyProfile } from "./use-profiles"

type Mode = "builder" | "yaml"

// noEffort stands for "the runtime's default" in the effort's group, whose values cannot be empty.
const noEffort = "default"

// Monaco is large, so it is fetched when the YAML tab first opens.
const YamlEditor = lazy(() => import("./yaml-editor"))

// ProfileEditor edits one profile, new or stored, and saves it as a version. It is Omnara's config editor
// (Apache License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/agents/
// AgentConfigEditor.tsx, AgentConfigBasicForm.tsx, AgentConfigModelField.tsx, AgentProfileConfigEditor.tsx,
// ConfirmDiscardYamlDialog.tsx): Builder and YAML as two pills, the effort joined to the model, "Save revision"
// off until something changed, "Discard changes", and a question before YAML edits are thrown away. The name that
// cannot change, the description and the runtime's select are Omnigent's form (Apache License 2.0,
// https://github.com/omnigent-ai/omnigent; web/src/pages/settings/CustomAgentConfigForm.tsx). The effort is a row
// of pills with "Default" first, as OpenMausBot's (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/ModelPicker.tsx, EffortRow). Changed: the builder does not read YAML back, so going back to it
// always discards YAML edits; every model is listed, and the server says which a runtime cannot use.
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
  // changed is what has not been saved. A new agent that starts as a copy (issue #200) has nothing to
  // lose until it is edited, yet can be created as it is: there is something to save while it is not empty.
  const changed = yaml !== null ? yaml !== (startSource ?? "") : !untouched
  const dirty = stored || yaml !== null ? changed : !sameDraft(draft, emptyDraft)
  const wrong = yaml === null ? problem(draft) : yaml.trim() === "" ? "The YAML is empty." : null

  const set = (change: Partial<Draft>) => {
    apply.reset()
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
        onSuccess: onSaved,
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
        <Builder
          draft={draft}
          set={set}
          models={models}
          renamable={!stored}
          access={stored?.tool_access}
        />
      ) : (
        <Field>
          <FieldLabel id="profile-yaml-label">Config (YAML)</FieldLabel>
          <Suspense fallback={<div className="h-[26rem] animate-pulse rounded-lg bg-muted" />}>
            <YamlEditor
              id="profile-yaml"
              label="Config (YAML)"
              value={shown}
              onChange={(next) => {
                if (next === shown) return
                apply.reset()
                setYaml(next)
              }}
            />
          </Suspense>
          <FieldDescription>
            The document the server stores. The editor marks what does not fit it as you type; the
            server checks the rest when you save.
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
        <Button
          type="submit"
          focusableWhenDisabled={apply.isPending}
          disabled={apply.isPending || !dirty || wrong !== null}
        >
          {apply.isPending && <Spinner data-icon="inline-start" />}
          {stored ? "Save revision" : "Create profile"}
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
  access,
}: {
  draft: Draft
  set: (change: Partial<Draft>) => void
  models: Model[]
  renamable: boolean
  access?: ToolAccess
}) {
  const runtimeChoices = routingRoles.includes(draft.name.trim()) ? ["role", ...runtimes] : runtimes

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
          placeholder="implementer"
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
        <Select
          value={draft.runtime || "role"}
          onValueChange={(runtime) =>
            runtime && set({ runtime: runtime === "role" ? "" : runtime })
          }
          items={runtimeChoices.map((r) => ({
            value: r,
            label: runtimeName(r === "role" ? "" : r),
          }))}
        >
          <SelectTrigger id="profile-runtime" width="full">
            <SelectValue>
              {(value: string) => <RuntimeName runtime={value === "role" ? "" : value} />}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            {runtimeChoices.map((r) => (
              <SelectItem key={r} value={r}>
                <RuntimeName runtime={r === "role" ? "" : r} />
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>
          {draft.runtime
            ? "The CLI a worker of this agent is, on a machine."
            : "The owner’s role routing selects the runtime, model and fallbacks for each Task kind."}
        </FieldDescription>
      </Field>

      <div className="flex flex-col gap-2">
        <div className="grid grid-cols-2 gap-3">
          <Field>
            <FieldLabel htmlFor="profile-cpu">CPU request</FieldLabel>
            <Input
              id="profile-cpu"
              placeholder="1"
              value={draft.cpu}
              onChange={(event) => set({ cpu: event.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="profile-memory">Memory request</FieldLabel>
            <Input
              id="profile-memory"
              placeholder="3Gi"
              value={draft.memory}
              onChange={(event) => set({ memory: event.target.value })}
            />
          </Field>
        </div>
        <FieldDescription>
          What a worker asks of a machine: a Task goes only where it fits. Cores (0.5) or millicores
          (500m); memory as 512Mi or 3Gi. Empty is 1 core and 3Gi, sized for building.
        </FieldDescription>
      </div>

      {draft.runtime && (
        <ModelField
          id="profile-model"
          model={draft.model}
          effort={draft.effort}
          models={models}
          onChange={set}
        />
      )}

      <Field>
        <FieldLabel htmlFor="profile-instructions">Instructions</FieldLabel>
        <Textarea
          id="profile-instructions"
          rows={7}
          placeholder="Review the change the task names. Report what is wrong or missing, with file and line."
          value={draft.instructions}
          onChange={(event) => set({ instructions: event.target.value })}
        />
        <FieldDescription>
          Added after the rules every worker gets, which it may not override. Optional.
        </FieldDescription>
      </Field>

      <SkillsField
        id="profile-skills"
        access={access?.skills}
        skills={draft.skills}
        off={draft.skillsOff}
        onChange={set}
        description="What this agent's workers get besides the built-in skills. A skill for everyone is on unless it is turned off here; a change makes a new revision."
      />

      <ToolsField
        id="profile-tools"
        preset={draft.preset}
        access={access}
        tools={draft.tools}
        off={draft.toolsOff}
        onChange={set}
        description="Hypomnema's tools this agent's workers may call, from their next task on. The CLI's own tools are not switched here."
      />

      {draft.runtime && (
        <FallbackModels
          id="profile-fallback"
          primary={draft.model}
          chain={draft.fallbacks}
          models={models}
          onChange={(fallbacks) => set({ fallbacks })}
          description="While this model cannot be served, a Task that names no model of its own runs the first of these that can, on the same runtime. None: the Task waits."
          missing="Choose a model to add fallbacks."
        />
      )}
    </div>
  )
}

// ModelField picks a model and its effort: every provider's models in one picker with a search (model-picker.tsx),
// grouped by provider, with the effort as a row of pills under it. The Bots' page uses it too (features/bots), where the default is pi's.
export function ModelField({
  id,
  model,
  effort,
  models,
  onChange,
  defaultLabel = "Runtime default",
  description = "Every provider's models are listed. Saving says so when this runtime cannot use the one chosen.",
  gone = "No provider offers this model any more. Pick another, or the runtime's default.",
}: {
  id: string
  // model is "provider/name", a bare name, or "" for the default (profiles-state.ts, modelKey).
  model: string
  effort: string
  models: Model[]
  onChange: (change: { model?: string; effort?: string }) => void
  defaultLabel?: string
  description?: string
  // gone is said in the description's place when no provider offers the model chosen.
  gone?: string
}) {
  const known = model === "" || models.some((m) => modelKey(m.provider, m.name) === model)

  return (
    <Field>
      <FieldLabel htmlFor={id}>Model</FieldLabel>
      <div>
        <ModelPicker
          id={id}
          model={model}
          models={models}
          defaultLabel={defaultLabel}
          onChange={(next) => onChange({ model: next })}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Brain className="size-4 text-muted-foreground" aria-hidden />
        <ToggleGroup
          variant="outline"
          size="sm"
          spacing={0}
          aria-label="Effort"
          value={[effort || noEffort]}
          onValueChange={(next) => {
            const chosen = next[0] as string | undefined
            if (chosen) onChange({ effort: chosen === noEffort ? "" : chosen })
          }}
        >
          {[{ value: noEffort, label: "Default" }, ...efforts].map((e) => (
            <ToggleGroupItem key={e.value} value={e.value}>
              {(effort || noEffort) === e.value ? (
                e.label
              ) : (
                <span className="font-normal text-muted-foreground">{e.label}</span>
              )}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </div>
      <FieldDescription>{known ? description : gone}</FieldDescription>
    </Field>
  )
}

// RuntimeName is a runtime as a choice shows it: its mark, then its name (docs/conventions.md: a thing that has a
// logo is shown with it).
function RuntimeName({ runtime }: { runtime: string }) {
  return (
    <span className="flex items-center gap-2">
      <RuntimeMark runtime={runtime} className="size-4" />
      {runtimeName(runtime)}
    </span>
  )
}
