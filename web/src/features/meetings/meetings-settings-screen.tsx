import { Link } from "@tanstack/react-router"
import { BookA, ChevronRight, Sparkles } from "lucide-react"

import type { Model } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { PageHeader, PageHeading } from "@/components/page-header"
import { SettingsSection } from "@/components/settings-page"
import { toast } from "@/components/ui/toast"
import { ModelPicker } from "@/features/agents/model-picker"
import { modelKey, splitModel } from "@/features/agents/profiles-state"
import { pickGroups } from "@/features/models/gateway-keys-state"
import { useGatewayModels, useProviders } from "@/features/models/use-models"

import { MeetingWordsField } from "./meeting-words-field"
import { useMeetingSettings, useSetMeetingSettings } from "./use-meetings"

// MeetingSettingsScreen is the meetings' own settings (issue #1121), opened from the gear on the Meetings page: the
// dictionary and the model that writes the notes, what is set up once for every meeting, kept out of the Settings
// menu as Hyprnote keeps its dictionary and AI settings out of its meeting list (apps/desktop/src/settings/index.tsx;
// owner, 2026-11-23). It was Settings' group Meetings until then (issue #762), a page each; the sections are those
// pages, unchanged.
export function MeetingSettingsScreen() {
  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <Link to="/meetings" className="text-muted-foreground hover:text-foreground">
              Meetings
            </Link>
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden />
            <span className="truncate">Settings</span>
          </span>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-8 sm:px-5">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-8 pt-2">
          <PageHeading
            title="Meeting settings"
            description="What every meeting is set up with: the words it is heard with and the model that writes its notes."
          />
          <DictionarySection />
          <NotesModelSection />
        </div>
      </div>
    </main>
  )
}

// DictionarySection was Settings → Dictionary (issues #747, #762): one list of names and jargon given to every
// transcription (Nojoin's glossary setting) and to the model that writes the notes (Hyprnote's preferred names),
// named as Hyprnote names it.
function DictionarySection() {
  return (
    <SettingsSection
      id="dictionary"
      icon={BookA}
      title="Dictionary"
      note="Names and jargon every recording is heard with, and its notes are written with, spelled as here. Yours, and the names added for you. A word you fix in a transcript joins yours."
    >
      <MeetingWordsField />
    </SettingsSection>
  )
}

// NotesModelSection was Settings → Notes model (issues #721, #762): the gateway model that writes a meeting's
// notes, as Hyprnote has its LLM connection, Meetily its provider and model and Nojoin its settings, picked with the
// single-model picker the Bots use (features/agents/model-picker.tsx).
function NotesModelSection() {
  const settings = useMeetingSettings()

  return (
    <SettingsSection
      id="notes-model"
      icon={Sparkles}
      title="Notes model"
      note="The model that writes every meeting's summary, decisions and actions: a chat model of the gateway. Without one, meetings are transcribed but have no notes."
    >
      {settings.isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : settings.error ? (
        <ErrorAlert error={settings.error} />
      ) : (
        <NotesModel chosen={settings.data.notes_model} />
      )}
    </SettingsSection>
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
