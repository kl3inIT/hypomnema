import { Copy, FileText, Pencil, Plus, Sparkles, Trash2, WholeWord } from "lucide-react"
import { useState } from "react"

import type { MeetingFormat, Model } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage, SettingsSection } from "@/components/settings-page"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { toast } from "@/components/ui/toast"
import { ModelPicker } from "@/features/agents/model-picker"
import { modelKey, splitModel } from "@/features/agents/profiles-state"
import { pickGroups } from "@/features/models/gateway-keys-state"
import { useGatewayModels, useProviders } from "@/features/models/use-models"

import { FormatEditor, type Editing } from "./format-editor"
import { MeetingWordsField } from "./meeting-words-field"
import { useDeleteFormat, useMeetingSettings, useSetMeetingSettings } from "./use-meetings"

// MeetingsSettingsScreen is Settings → Meetings (issues #721, #747): the gateway model that writes a meeting's notes,
// as Hyprnote has its LLM connection, Meetily its provider and model and Nojoin its settings, picked with the
// single-model picker the Bots use (features/agents/model-picker.tsx); the words to hear right, one list given to
// every transcription (Nojoin's glossary setting); and the notes formats, each a list of sections fed to the prompt:
// the built-in ones (Hyprnote's and Meetily's templates, MIT) copied to be edited, the owner's made, changed and
// deleted, as Hyprnote's templates are.
export function MeetingsSettingsScreen() {
  const settings = useMeetingSettings()

  return (
    <SettingsPage
      title="Meetings"
      description="The model that writes a meeting's notes, the words every recording is heard with, and the formats the notes are written in."
    >
      <SettingsSection
        id="notes-model"
        icon={Sparkles}
        title="Notes model"
        note="A chat model of the gateway. It writes the summary, decisions and actions of every meeting."
      >
        {settings.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : settings.error ? (
          <ErrorAlert error={settings.error} />
        ) : (
          <NotesModel chosen={settings.data.notes_model} />
        )}
      </SettingsSection>
      <SettingsSection
        id="words"
        icon={WholeWord}
        title="Words to hear right"
        note="Names and jargon the speech-to-text provider is given with every recording, so it spells them right."
      >
        <MeetingWordsField />
      </SettingsSection>
      <SettingsSection
        id="formats"
        icon={FileText}
        title="Notes formats"
        note="Every format has the summary, decisions and actions; each adds its sections. Pick one on upload or with Write again."
      >
        {settings.data && <Formats formats={settings.data.formats} />}
      </SettingsSection>
    </SettingsPage>
  )
}

// NotesModel is the one model that writes notes, picked as a Bot's model is: the gateway's chat models grouped by
// the provider that serves them, and "None" first.
function NotesModel({ chosen }: { chosen: string }) {
  const set = useSetMeetingSettings()
  const models = useGatewayModels()
  const providers = useProviders()
  const offered: Model[] = pickGroups(models.data ?? [], providers.data ?? []).flatMap((g) =>
    g.models.map((m) => ({ provider: g.provider, name: m.id, kind: g.kind })),
  )
  const known = offered.find((m) => m.name === chosen)

  return (
    <div className="flex flex-col gap-2" data-testid="notes-model">
      <ModelPicker
        id="notes-model-picker"
        model={known ? modelKey(known.provider, known.name) : chosen}
        models={offered}
        defaultLabel="None: notes are not written"
        onChange={(key) =>
          set.mutate(
            { body: { notes_model: key ? splitModel(key, offered).name : "" } },
            {
              onSuccess: (s) =>
                toast.add({
                  title: s.notes_model ? `Notes are written by ${s.notes_model}` : "No notes model",
                  type: "success",
                }),
            },
          )
        }
      />
      <ErrorAlert error={set.error} />
    </div>
  )
}

function Formats({ formats }: { formats: MeetingFormat[] }) {
  const [editing, setEditing] = useState<Editing | null>(null)
  const own = formats.filter((f) => !f.builtin)
  const builtin = formats.filter((f) => f.builtin)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <h3 className="me-auto text-xs font-medium text-muted-foreground">
            Yours · {own.length}
          </h3>
          <Button size="sm" variant="outline" onClick={() => setEditing({ mode: "new" })}>
            <Plus data-icon="inline-start" />
            New format
          </Button>
        </div>
        {own.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            None yet. Make one, or copy a built-in one to change it.
          </p>
        ) : (
          <ul aria-label="Your formats" className="grid gap-2 sm:grid-cols-2">
            {own.map((f) => (
              <FormatCard
                key={f.id}
                format={f}
                onEdit={() => setEditing({ mode: "edit", format: f })}
              />
            ))}
          </ul>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <h3 className="text-xs font-medium text-muted-foreground">Built in · {builtin.length}</h3>
        <ul aria-label="Built-in formats" className="grid gap-2 sm:grid-cols-2">
          {builtin.map((f) => (
            <FormatCard
              key={f.id}
              format={f}
              onCopy={() => setEditing({ mode: "copy", format: f })}
            />
          ))}
        </ul>
      </div>
      <FormatEditor editing={editing} onClose={() => setEditing(null)} />
    </div>
  )
}

// FormatCard is one format: its name and its sections; the owner's has Edit and Delete, a built-in one Copy, as
// Hyprnote's built-in template offers only Duplicate.
function FormatCard({
  format,
  onEdit,
  onCopy,
}: {
  format: MeetingFormat
  onEdit?: () => void
  onCopy?: () => void
}) {
  const remove = useDeleteFormat()

  return (
    <li
      data-testid="format"
      aria-label={format.name}
      className="flex flex-col gap-1.5 rounded-lg border bg-card px-3 py-2.5"
    >
      <div className="flex items-start gap-2">
        <p className="me-auto min-w-0 text-sm font-medium">{format.name}</p>
        {!format.builtin && <Badge variant="secondary">Yours</Badge>}
      </div>
      <p className="text-xs text-muted-foreground">
        {format.sections.map((s) => s.title).join(" · ")}
      </p>
      <div className="mt-auto flex flex-wrap gap-1 pt-1">
        {onCopy && (
          <Button
            size="xs"
            variant="ghost"
            onClick={onCopy}
            aria-label={`Copy ${format.name} to edit`}
          >
            <Copy data-icon="inline-start" />
            Copy to edit
          </Button>
        )}
        {onEdit && (
          <>
            <Button size="xs" variant="ghost" onClick={onEdit} aria-label={`Edit ${format.name}`}>
              <Pencil data-icon="inline-start" />
              Edit
            </Button>
            <ConfirmDialog
              trigger={
                <Button size="xs" variant="ghost" aria-label={`Delete ${format.name}`}>
                  <Trash2 data-icon="inline-start" />
                  Delete
                </Button>
              }
              title={`Delete ${format.name}?`}
              consequence="Notes already written in it keep their sections. It is no longer offered on upload or with Write again."
              action="Delete format"
              run={() => remove.mutateAsync({ path: { format_id: format.id } })}
              onDone={() => undefined}
              done={`Deleted ${format.name}`}
            />
          </>
        )}
      </div>
    </li>
  )
}
