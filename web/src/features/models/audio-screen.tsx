import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import {
  AudioLines,
  Box,
  Clock,
  GripVertical,
  KeyRound,
  Mic,
  Plus,
  Power,
  Timer,
  Trash2,
  type LucideIcon,
} from "lucide-react"
import { useId, useState, type DragEvent, type ReactNode } from "react"
import { z } from "zod"

import "@/api/client"
import {
  addAudioKey,
  removeAudioKey,
  setAudioOrder,
  setAudioProvider,
} from "@/api/generated/sdk.gen"
import type {
  Audio,
  AudioKey,
  AudioKind,
  AudioKindLink,
  AudioOrder,
  AudioProvider,
  AudioProviderLink,
} from "@/api/generated/types.gen"
import { AudioProviderMark } from "@/components/icons/provider-marks"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { FieldGroup } from "@/components/ui/field"
import { Item, ItemActions, ItemContent, ItemDescription, ItemTitle } from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsSection, SettingsPage } from "@/components/settings-page"
import { TextField } from "@/components/text-field"
import { said } from "@/lib/said"
import { since } from "@/lib/time"

import {
  audioKeyPages,
  audioKeyState,
  audioProviderName,
  audioState,
  audioUsageText,
  kindName,
} from "./audio-state"
import { useAudio, useShowAudio } from "./use-models"
import { moved } from "./web-search-state"

// AudioScreen is Settings → Gateway → Audio (issue #694), built from the Web search and Images pages: first the
// providers with the owner's keys, as the Web search page lists its keyed providers (web-search-screen.tsx): each
// with its state, its switch for both kinds, a key added in a dialog as OmniRoute's and 9router's "Add API Key"
// modals take one, and its keys with their rests; then speech to text and text to speech, each in the order it asks
// the providers, dragged by a handle as the Images page orders its providers (images-screen.tsx), each with its one
// model and what it heard or spoke. The page does what `hypomnema audio providers|keys|order` does.
export function AudioScreen() {
  const audio = useAudio()

  return (
    <SettingsPage
      title="Audio"
      description="How agents turn speech into text and text into speech: your providers' keys, and the order each kind asks them in."
    >
      <ErrorAlert error={audio.error} />
      {audio.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        audio.data && <AudioSettings audio={audio.data} />
      )}
    </SettingsPage>
  )
}

function AudioSettings({ audio }: { audio: Audio }) {
  const links = new Map(audio.providers.map((l) => [l.provider, l]))

  return (
    <div className="flex flex-col gap-8">
      <SettingsSection
        id="audio-providers"
        icon={KeyRound}
        title="Providers"
        note="Each is asked with your API keys, the one used longest ago first. A key that is rate limited, refused or out of credits rests on its own while the others go on. A provider's switch is for both kinds."
      >
        <div className="flex flex-col gap-2.5">
          {audio.providers.map((link) => (
            <ProviderRow key={link.provider} link={link} />
          ))}
        </div>
      </SettingsSection>
      <SettingsSection
        id="audio-stt"
        icon={Mic}
        title="Speech to text"
        note="Asked in this order; drag a provider by its handle to change it. A recording is heard by the first that answers; a Soniox job is waited for up to 30 minutes."
      >
        <KindList kind="stt" order={audio.stt} links={links} />
      </SettingsSection>
      <SettingsSection
        id="audio-tts"
        icon={AudioLines}
        title="Text to speech"
        note="Asked in this order; drag a provider by its handle to change it."
      >
        <KindList kind="tts" order={audio.tts} links={links} />
      </SettingsSection>
    </div>
  )
}

// useChange runs a change and shows the providers the server answers with.
function useChange<T>(change: (input: T) => Promise<Audio>, done: (input: T) => string) {
  const show = useShowAudio()

  return useMutation({
    mutationFn: change,
    onSuccess: (audio, input) => {
      show(audio)
      said(done(input))
    },
  })
}

// ProviderRow is one provider: its mark, its state, a key to add, its switch, and its keys.
function ProviderRow({ link }: { link: AudioProviderLink }) {
  const name = audioProviderName(link.provider)
  const state = audioState(link)
  const toggle = useChange(
    async (on: boolean) =>
      (await setAudioProvider({ path: { provider: link.provider }, body: { on } })).data,
    (on) => (on ? `${name} is on` : `${name} is off`),
  )

  return (
    <div className={link.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={name}>
        <AudioProviderMark provider={link.provider} className="size-5 shrink-0 self-start" />
        <ItemContent>
          <ItemTitle>{name}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <StateBadge state={state} icon={stateIcon(link.state)} />
              {link.keys.length > 0 && (
                <Fact icon={KeyRound}>
                  {link.keys.length === 1 ? "1 key" : `${link.keys.length} keys`}
                </Fact>
              )}
            </span>
          </ItemDescription>
          <ErrorAlert error={toggle.error} />
        </ItemContent>
        <div className="self-start">
          <ItemActions>
            <AddKeyDialog provider={link.provider} first={link.keys.length === 0} />
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <label htmlFor={`use-${link.provider}`} className="inline-flex items-center gap-1">
                <Power className="size-3" aria-hidden />
                In use
              </label>
              <Switch
                id={`use-${link.provider}`}
                size="sm"
                checked={link.on}
                disabled={toggle.isPending}
                onCheckedChange={(on) => toggle.mutate(on)}
              />
            </span>
          </ItemActions>
        </div>
      </Item>
      {link.keys.length > 0 && (
        <ul
          className="ms-6 mt-1.5 flex flex-col gap-1.5 border-s ps-3"
          aria-label={`${name}'s keys`}
        >
          {link.keys.map((key) => (
            <KeyRow key={key.name} provider={link.provider} keyed={key} />
          ))}
        </ul>
      )}
    </div>
  )
}

// KeyRow is one key: its name in the vault, its state, when it was last used, and its removal.
function KeyRow({ provider, keyed }: { provider: AudioProvider; keyed: AudioKey }) {
  const show = useShowAudio()
  const state = audioKeyState(keyed)

  return (
    <li
      aria-label={keyed.name}
      className="flex flex-col gap-1.5 rounded-md border px-3 py-2 text-xs sm:flex-row sm:items-center sm:gap-3"
    >
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
        <span className="truncate font-mono text-foreground">{keyed.name}</span>
        <StateBadge state={state} icon={keyed.state === "ready" ? undefined : Timer} />
        {keyed.last_used_at && <Fact icon={Clock}>Used {since(keyed.last_used_at)}</Fact>}
      </span>
      <span className="self-end sm:self-auto">
        <ConfirmDialog
          trigger={
            <Button variant="ghost" size="sm" aria-label={`Remove ${keyed.name}`}>
              <Trash2 data-icon="inline-start" />
              Remove
            </Button>
          }
          title={`Remove ${keyed.name}?`}
          consequence={`It is deleted from the vault and ${audioProviderName(provider)} is no longer asked with it.`}
          action="Remove"
          run={async () =>
            show((await removeAudioKey({ path: { provider, name: keyed.name } })).data)
          }
          onDone={() => undefined}
          done={`${keyed.name} was removed`}
        />
      </span>
    </li>
  )
}

const keySchema = z.object({ key: z.string().trim().min(1, "Paste the key.") })

// AddKeyDialog takes one more key of a provider into the vault: a password field, never filled with a key kept.
function AddKeyDialog({ provider, first }: { provider: AudioProvider; first: boolean }) {
  const formId = useId()
  const [open, setOpen] = useState(false)
  const name = audioProviderName(provider)
  const show = useShowAudio()
  const save = useMutation({
    mutationFn: async (key: string) =>
      (await addAudioKey({ path: { provider }, body: { key } })).data,
    onSuccess: (added) => {
      show(added.audio)
      said(`${name}'s key is kept in the vault as ${added.name}`)
    },
  })
  const form = useForm({
    defaultValues: { key: "" },
    validators: { onSubmit: keySchema },
    onSubmit: async ({ value }) => {
      await save.mutateAsync(value.key.trim())
      setOpen(false)
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) {
          form.reset()
          save.reset()
        }
      }}
    >
      <DialogTrigger
        render={
          <Button variant={first ? "outline" : "ghost"} size="sm" aria-label={`Add a ${name} key`}>
            {first ? <KeyRound data-icon="inline-start" /> : <Plus data-icon="inline-start" />}
            Add key
          </Button>
        }
      />
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{name} API key</DialogTitle>
          <DialogDescription>
            {first
              ? `With a key, ${name} hears recordings and speaks text for your agents.`
              : `One more key of your own ${name} account; the keys are used in turn.`}{" "}
            Get one at{" "}
            <a
              href={audioKeyPages[provider]}
              target="_blank"
              rel="noreferrer"
              className="underline underline-offset-4"
            >
              {new URL(audioKeyPages[provider]).host}
            </a>
            .
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <FieldGroup>
              <form.Field name="key">
                {(field) => (
                  <TextField
                    field={field}
                    label="API key"
                    secret
                    placeholder="Paste the key"
                    description="It is kept in the vault and never shown again."
                  />
                )}
              </form.Field>
            </FieldGroup>
          </form>
        </DialogPanel>
        <ErrorAlert error={save.error} />
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button
            type="submit"
            form={formId}
            focusableWhenDisabled={save.isPending}
            disabled={save.isPending}
          >
            {save.isPending && <Spinner data-icon="inline-start" />}
            Save key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// KindList is one kind's providers in its order. One is dragged by its handle and dropped on another's place; the
// new order is sent at once and the server's answer shown.
function KindList({
  kind,
  order,
  links,
}: {
  kind: AudioKind
  order: AudioKindLink[]
  links: Map<AudioProvider, AudioProviderLink>
}) {
  const [dragged, setDragged] = useState<AudioProvider | null>(null)
  const [over, setOver] = useState<AudioProvider | null>(null)
  const providers = order.map((l) => l.provider)
  const reorder = useChange(
    async (next: AudioProvider[]) =>
      (await setAudioOrder({ path: { kind }, body: { providers: next } as AudioOrder })).data,
    (next) =>
      next[0]
        ? `${audioProviderName(next[0])} is asked first for ${kindName(kind)}`
        : "The order is kept",
  )

  const drop = (to: AudioProvider) => {
    const from = dragged
    setDragged(null)
    setOver(null)

    if (from && from !== to) reorder.mutate(moved(providers, from, to))
  }

  return (
    <div className="flex flex-col gap-2.5">
      <ErrorAlert error={reorder.error} />
      {order.map((entry, index) => {
        const link = links.get(entry.provider)

        return (
          <div
            key={entry.provider}
            data-dragging={dragged === entry.provider || undefined}
            data-over={(over === entry.provider && dragged !== entry.provider) || undefined}
            className="rounded-lg outline-offset-2 data-dragging:opacity-50 data-over:outline-2 data-over:outline-ring"
            onDragOver={(event: DragEvent) => {
              if (!dragged) return
              event.preventDefault()
              setOver(entry.provider)
            }}
            onDragLeave={() => setOver((now) => (now === entry.provider ? null : now))}
            onDrop={(event: DragEvent) => {
              event.preventDefault()
              drop(entry.provider)
            }}
          >
            <KindRow
              kind={kind}
              entry={entry}
              link={link}
              place={index + 1}
              busy={reorder.isPending}
              onDragStart={() => setDragged(entry.provider)}
              onDragEnd={() => {
                setDragged(null)
                setOver(null)
              }}
            />
          </div>
        )
      })}
    </div>
  )
}

// KindRow is one provider in a kind's order: its place, its mark, its state, its model and what it heard or spoke.
function KindRow({
  kind,
  entry,
  link,
  place,
  busy,
  onDragStart,
  onDragEnd,
}: {
  kind: AudioKind
  entry: AudioKindLink
  link: AudioProviderLink | undefined
  place: number
  busy: boolean
  onDragStart: () => void
  onDragEnd: () => void
}) {
  const name = audioProviderName(entry.provider)

  return (
    <div className={entry.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={`${name}, ${kindName(kind)}`}>
        {/* The handle is its own design: a grip that is dragged, not pressed (search-rotation's .handle). */}
        <span
          draggable={!busy}
          onDragStart={(event) => {
            event.dataTransfer.effectAllowed = "move"
            event.dataTransfer.setData("text/plain", entry.provider)
            onDragStart()
          }}
          onDragEnd={onDragEnd}
          className="-ms-1 cursor-grab self-start pt-0.5 text-muted-foreground active:cursor-grabbing pointer-coarse:hidden"
          title="Drag to change the order"
          aria-label={`Drag ${name} to change the order of ${kindName(kind)}`}
        >
          <GripVertical className="size-4" aria-hidden />
        </span>
        <span
          className="self-start pt-0.5 text-xs text-muted-foreground tabular-nums"
          title="A request asks the providers in this order"
        >
          #{place}
        </span>
        <AudioProviderMark provider={entry.provider} className="size-5 shrink-0 self-start" />
        <ItemContent>
          <ItemTitle>{name}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              {link && <StateBadge state={audioState(link)} icon={stateIcon(link.state)} />}
              <Fact icon={Box} title="The model it is asked with">
                <span className="font-mono">{entry.model}</span>
              </Fact>
              {/* The usage wraps on a phone rather than push the row's name off its line. */}
              <span
                className="inline-flex min-w-0 items-start gap-1"
                title="This control plane's requests, by UTC day and month"
              >
                {kind === "stt" ? (
                  <Mic className="mt-0.5 size-3 shrink-0" aria-hidden />
                ) : (
                  <AudioLines className="mt-0.5 size-3 shrink-0" aria-hidden />
                )}
                <span className="tabular-nums">{audioUsageText(kind, entry.usage)}</span>
              </span>
            </span>
          </ItemDescription>
        </ItemContent>
      </Item>
    </div>
  )
}

function StateBadge({
  state,
  icon: Icon,
}: {
  state: ReturnType<typeof audioState>
  icon?: LucideIcon
}) {
  return (
    <Badge variant={state.tone} title={state.why}>
      {Icon && <Icon aria-hidden />}
      {state.label}
    </Badge>
  )
}

function stateIcon(state: AudioProviderLink["state"]): LucideIcon | undefined {
  if (state === "resting") return Timer
  if (state === "off") return Power

  return undefined
}

function Fact({
  icon: Icon,
  title,
  children,
}: {
  icon: LucideIcon
  title?: string
  children: ReactNode
}) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap" title={title}>
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}
