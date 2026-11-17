import { FileText, Sparkles } from "lucide-react"

import { ErrorAlert } from "@/components/error-alert"
import { ModelMark } from "@/components/icons/model-mark"
import { SettingsPage, SettingsSection } from "@/components/settings-page"
import { toast } from "@/components/ui/toast"
import { pickGroups } from "@/features/models/gateway-keys-state"
import { GatewayModelPicker } from "@/features/models/gateway-model-picker"
import { useGatewayModels, useProviders } from "@/features/models/use-models"

import { useMeetingSettings, useSetMeetingSettings } from "./use-meetings"

// MeetingsSettingsScreen is Settings → Meetings (issue #721): the gateway model that writes a meeting's notes, as
// Hyprnote has its LLM connection, Meetily its provider and model and Nojoin its settings, picked with the gateway
// keys' model picker; and the notes formats, each a list of sections fed to the prompt (Hyprnote's and Meetily's
// templates, MIT).
export function MeetingsSettingsScreen() {
  const settings = useMeetingSettings()
  const set = useSetMeetingSettings()
  const models = useGatewayModels()
  const providers = useProviders()
  const groups = pickGroups(models.data ?? [], providers.data ?? [])
  const chosen = settings.data?.notes_model ?? ""

  return (
    <SettingsPage
      title="Meetings"
      description="The model that writes a meeting's notes from its transcript, and the formats it writes them in."
    >
      <SettingsSection
        id="notes-model"
        icon={Sparkles}
        title="Notes model"
        note="A chat model of the gateway. It writes the summary, decisions and actions of every meeting."
      >
        {settings.isPending || models.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : settings.error ? (
          <ErrorAlert error={settings.error} />
        ) : (
          <>
            <p className="flex items-center gap-2 text-sm" data-testid="notes-model">
              {chosen ? (
                <>
                  <ModelMark name={chosen} className="size-4.5 shrink-0" />
                  <span className="font-medium">{chosen}</span>
                </>
              ) : (
                <span className="text-muted-foreground">
                  None picked: notes are not written until one is.
                </span>
              )}
            </p>
            <GatewayModelPicker
              single
              groups={groups}
              chosen={chosen ? [chosen] : []}
              onChange={(next) =>
                set.mutate(
                  { body: { notes_model: next[0] ?? "" } },
                  {
                    onSuccess: (s) =>
                      toast.add({
                        title: s.notes_model
                          ? `Notes are written by ${s.notes_model}`
                          : "No notes model",
                        type: "success",
                      }),
                  },
                )
              }
            />
            <ErrorAlert error={set.error} />
          </>
        )}
      </SettingsSection>
      <SettingsSection
        id="formats"
        icon={FileText}
        title="Notes formats"
        note="Every format has the summary, decisions and actions; each adds these sections. Pick one on upload or with Write again."
      >
        <ul className="grid gap-2 sm:grid-cols-2">
          {(settings.data?.formats ?? []).map((f) => (
            <li key={f.id} className="rounded-lg border bg-card px-3 py-2.5">
              <p className="text-sm font-medium">{f.name}</p>
              <p className="text-xs text-muted-foreground">
                {f.sections.map((s) => s.title).join(" · ")}
              </p>
            </li>
          ))}
        </ul>
      </SettingsSection>
    </SettingsPage>
  )
}
