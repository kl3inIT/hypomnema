import { BookA, Sparkles } from "lucide-react"

import type { Model } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage, SettingsSection } from "@/components/settings-page"
import { toast } from "@/components/ui/toast"
import { ModelPicker } from "@/features/agents/model-picker"
import { modelKey, splitModel } from "@/features/agents/profiles-state"
import { pickGroups } from "@/features/models/gateway-keys-state"
import { useGatewayModels, useProviders } from "@/features/models/use-models"

import { MeetingWordsField } from "./meeting-words-field"
import { useMeetingSettings, useSetMeetingSettings } from "./use-meetings"

// The group Meetings of Settings (issue #762), out of Gateway, which holds the gateway's models, keys and providers:
// what is set up once for every meeting, a page each, as Hyprnote's Settings has Dictionary
// (apps/desktop/src/settings/dictionary/index.tsx) and its LLM under AI (settings/ai/llm).

// DictionarySettingsScreen is Settings → Dictionary (issues #747, #762): one list of names and jargon given to every
// transcription (Nojoin's glossary setting) and to the model that writes the notes (Hyprnote's preferred names),
// named as Hyprnote names it.
export function DictionarySettingsScreen() {
  return (
    <SettingsPage
      title="Dictionary"
      description="Names and jargon every recording is heard with, and its notes are written with, spelled as here."
    >
      <SettingsSection
        id="dictionary"
        icon={BookA}
        title="Words"
        note="Yours, and the names added for you. A word you fix in a transcript joins yours."
      >
        <MeetingWordsField />
      </SettingsSection>
    </SettingsPage>
  )
}

// NotesModelSettingsScreen is Settings → Notes model (issues #721, #762): the gateway model that writes a meeting's
// notes, as Hyprnote has its LLM connection, Meetily its provider and model and Nojoin its settings, picked with the
// single-model picker the Bots use (features/agents/model-picker.tsx).
export function NotesModelSettingsScreen() {
  const settings = useMeetingSettings()

  return (
    <SettingsPage
      title="Notes model"
      description="The model that writes every meeting's summary, decisions and actions."
    >
      <SettingsSection
        id="notes-model"
        icon={Sparkles}
        title="Model"
        note="A chat model of the gateway. Without one, meetings are transcribed but have no notes."
      >
        {settings.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : settings.error ? (
          <ErrorAlert error={settings.error} />
        ) : (
          <NotesModel chosen={settings.data.notes_model} />
        )}
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
