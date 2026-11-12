import { useQueryClient } from "@tanstack/react-query"
import { Link, useBlocker, useNavigate } from "@tanstack/react-router"
import { ChevronRight } from "lucide-react"
import { useRef, useState } from "react"

import type { Bot, Model } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button, buttonVariants } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { ModelField } from "@/features/agents/profile-editor"
import { errorText } from "@/features/agents/profiles-state"
import { useModels } from "@/features/agents/use-profiles"
import { LarkIcon } from "@/components/icons/brand-icons"
import { channelsOf, channelState } from "@/features/settings/channels-state"
import { ConfirmDialog } from "@/features/settings/settings-parts"
import { useLarkChannels } from "@/features/settings/use-channels"
import { PageHeader } from "@/features/shell/app-shell"

import {
  draftOf,
  emptyDraft,
  memoryEntries,
  memoryLine,
  modelLine,
  problem,
  sameDraft,
  settingsOf,
  type Draft,
} from "./bots-state"
import { botsKey, useBot, useBots, useDeleteBot, usePutBot, useSetBotMemory } from "./use-bots"

// Crumbs is the way back to the list, as the Agents pages' "Agents › name".
function Crumbs({ here }: { here: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link to="/bots" className="text-muted-foreground hover:text-foreground">
        Bots
      </Link>
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden />
      <span className="truncate">{here}</span>
    </span>
  )
}

// BotScreen is one Bot (issue #169): its name and model, the form for its model and persona, its memory to read
// and edit, and its removal. The page is the Agents pages' profile page (features/agents/profile-screen.tsx).
export function BotScreen({ name }: { name: string }) {
  const bot = useBot(name)
  const models = useModels()
  const b = bot.data

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here={name} />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-2xl flex-col gap-5 pt-2">
          {bot.error ? (
            <div className="flex flex-col items-center gap-3 py-10 text-sm text-muted-foreground">
              <p>This Bot could not be read. It may not exist.</p>
              <Link to="/bots" className={buttonVariants({ variant: "outline", size: "sm" })}>
                Back to Bots
              </Link>
            </div>
          ) : !b ? (
            <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
          ) : (
            <>
              <header className="flex items-center gap-3.5">
                <AgentAvatar name={b.name} className="size-12 shrink-0" />
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <h2 className="truncate text-lg font-semibold tracking-tight">{b.name}</h2>
                  <p className="text-xs text-muted-foreground">
                    Bot · {modelLine(b)}
                    {b.provider ? ` (${b.provider})` : ""}
                  </p>
                </div>
              </header>

              <BotForm start={draftOf(b)} models={models.data ?? []} stored={b} />
              <Memory bot={b} />
              <Channels bot={b} />
              <Removal bot={b} />
            </>
          )}
        </div>
      </div>
    </main>
  )
}

// BotForm edits a Bot's model and persona, and a new Bot's name, and saves them with one PUT. Its buttons and its
// guard against leaving unsaved changes are the profile editor's (features/agents/profile-editor.tsx).
function BotForm({
  start: first,
  models,
  stored,
  onSaved,
}: {
  start: Draft
  models: Model[]
  // stored is the Bot being edited; absent for a new one, whose name can be typed.
  stored?: Bot
  onSaved?: (bot: Bot) => void
}) {
  // start is what the server holds: the Bot as it was read, then as each save stored it.
  const [start, setStart] = useState(first)
  const [draft, setDraft] = useState(first)
  const put = usePutBot()
  // The names taken, so that a new Bot does not replace one that exists: PUT changes a Bot of the same name.
  const taken = (useBots().data ?? []).map((b) => b.name)

  const dirty = !sameDraft(draft, start)
  const wrong = stored ? null : problem(draft, taken)
  const saved = useRef(false)

  useBlocker({
    shouldBlockFn: () =>
      !saved.current && !window.confirm("You have unsaved changes. Discard them?"),
    enableBeforeUnload: () => !saved.current,
    disabled: !dirty,
  })

  const set = (change: Partial<Draft>) => {
    put.reset()
    saved.current = false
    setDraft((d) => ({ ...d, ...change }))
  }

  function save() {
    put.mutate(
      { name: stored?.name ?? draft.name.trim(), settings: settingsOf(draft, models) },
      {
        onSuccess: (bot) => {
          saved.current = true
          setStart(draftOf(bot))
          setDraft(draftOf(bot))
          onSaved?.(bot)
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
      {!stored && (
        <Field>
          <FieldLabel htmlFor="bot-name">Name</FieldLabel>
          <Input
            id="bot-name"
            required
            autoComplete="off"
            spellCheck={false}
            placeholder="researcher"
            value={draft.name}
            onChange={(event) => set({ name: event.target.value })}
          />
          <FieldDescription>
            A chat names its Bot by this. Lowercase letters, digits and dashes, starting with a
            letter. It cannot be changed later.
          </FieldDescription>
        </Field>
      )}

      <ModelField
        id="bot-model"
        model={draft.model}
        effort={draft.effort}
        models={models}
        onChange={set}
        defaultLabel="pi's default model"
        description="The model the Bot thinks with. Saving says so when pi cannot use the one chosen."
        gone="No provider offers this model any more. Pick another, or pi's default."
      />

      <Field>
        <FieldLabel htmlFor="bot-persona">Persona</FieldLabel>
        <Textarea
          id="bot-persona"
          rows={8}
          placeholder="You are the team's researcher. Answer briefly, with sources."
          value={draft.persona}
          onChange={(event) => set({ persona: event.target.value })}
        />
        <FieldDescription>
          What the Bot is and how it behaves. It gets this after its built-in instructions, in every
          chat. Optional.
        </FieldDescription>
      </Field>

      {put.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(put.error)}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
        {wrong && dirty && <p className="me-auto text-xs text-muted-foreground">{wrong}</p>}
        {dirty && !put.isPending && stored && (
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              put.reset()
              setDraft(start)
            }}
          >
            Discard changes
          </Button>
        )}
        <Button type="submit" disabled={put.isPending || !dirty || wrong !== null}>
          {stored
            ? put.isPending
              ? "Saving…"
              : "Save"
            : put.isPending
              ? "Creating…"
              : "Create Bot"}
        </Button>
      </div>
    </form>
  )
}

// Memory is what the Bot keeps across chats, an entry a line. The Bot writes it itself, an entry at a time; the
// owner reads it here and edits it by hand, as the owner of an agent of OpenClaw, Hermes or OpenMausBot edits its
// memory file.
function Memory({ bot }: { bot: Bot }) {
  // base is the memory the server holds, as last read or saved; draft is what the owner sees and types.
  const [base, setBase] = useState(bot.memory)
  const [draft, setDraft] = useState(bot.memory)
  const save = useSetBotMemory()

  const dirty = draft !== base
  // The Bot wrote its memory meanwhile: an untouched field follows it; one being edited is kept and says so.
  const changedUnder = bot.memory !== base
  if (changedUnder && !dirty) {
    setBase(bot.memory)
    setDraft(bot.memory)
  }

  const used = [...memoryEntries(draft).join("\n")].length
  const over = used > bot.memory_limit

  return (
    <form
      aria-labelledby="bot-memory"
      className="flex flex-col gap-2 border-t pt-4"
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate(
          { name: bot.name, memory: draft },
          {
            onSuccess: (stored) => {
              setBase(stored.memory)
              setDraft(stored.memory)
            },
          },
        )
      }}
    >
      <div className="flex flex-wrap items-baseline gap-x-3">
        <label id="bot-memory" htmlFor="bot-memory-text" className="text-sm font-medium">
          Memory
        </label>
        <span className={over ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
          {dirty
            ? `${used.toLocaleString("en-US")} of ${bot.memory_limit.toLocaleString("en-US")} characters`
            : memoryLine(bot)}
        </span>
      </div>
      <Textarea
        id="bot-memory-text"
        rows={Math.min(12, Math.max(3, memoryEntries(draft).length + 1))}
        placeholder="Nothing kept yet."
        value={draft}
        onChange={(event) => {
          save.reset()
          setDraft(event.target.value)
        }}
      />
      <p className="text-xs text-muted-foreground">
        What the Bot keeps across its chats, one entry a line. It writes this itself; you can edit
        it. The Bot sees a change from its next chat on.
      </p>
      {changedUnder && dirty && (
        <p className="text-xs text-muted-foreground">
          The Bot changed its memory while you were editing. Saving replaces it with what is here.
        </p>
      )}
      {save.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(save.error)}</AlertDescription>
        </Alert>
      )}
      {dirty && (
        <div className="flex flex-wrap items-center justify-end gap-2">
          {!save.isPending && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                save.reset()
                setBase(bot.memory)
                setDraft(bot.memory)
              }}
            >
              Discard changes
            </Button>
          )}
          <Button type="submit" disabled={save.isPending || over}>
            {save.isPending ? "Saving…" : "Save memory"}
          </Button>
        </div>
      )}
    </form>
  )
}

// Channels are the doors to this Bot from outside the web client: the Lark Channels that talk to it, each a link
// to the Channels page, where they are set up. Only the owner may read them; for anyone else the list is left
// out.
function Channels({ bot }: { bot: Bot }) {
  const channels = useLarkChannels()
  if (!channels.data) return null

  const mine = channelsOf(bot.name, channels.data)

  return (
    <section aria-labelledby="bot-channels" className="flex flex-col gap-2 border-t pt-4">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <h3 id="bot-channels" className="text-sm font-medium">
          Channels
        </h3>
        <Link
          to="/settings/channels"
          className="text-xs text-muted-foreground hover:text-foreground hover:underline"
        >
          Manage channels
        </Link>
      </div>
      {mine.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No channel talks to this Bot: it is reached from its chats here only.
        </p>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {mine.map((channel) => (
            <li key={channel.name} className="flex min-w-0 items-center gap-2 text-sm">
              <LarkIcon className="size-4 shrink-0" />
              <Link to="/settings/channels" className="truncate font-medium hover:underline">
                {channel.name}
              </Link>
              <span className="shrink-0 text-xs text-muted-foreground">
                Lark · {channelState(channel).label}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// Removal takes a Bot away after a question. The server refuses the first Bot, one with chats, one with a
// memory and one a Channel talks to, and the dialog stays open with its reason.
function Removal({ bot }: { bot: Bot }) {
  const remove = useDeleteBot()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  return (
    <section
      aria-label="Remove"
      className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t pt-4"
    >
      <p className="min-w-0 flex-1 basis-64 text-xs text-muted-foreground">
        A Bot can be removed while it has no chats and no memory, and no channel talks to it.
      </p>
      <ConfirmDialog
        trigger={
          <Button variant="outline" size="sm">
            Remove Bot
          </Button>
        }
        title={`Remove ${bot.name}?`}
        consequence="The Bot, its model and its persona are removed. This cannot be undone."
        action="Remove"
        run={() => remove.mutateAsync(bot.name)}
        onDone={async () => {
          // The list is read again before the page leaves; this Bot's own query goes with its page.
          await queryClient.invalidateQueries({ queryKey: botsKey, exact: true })
          await navigate({ to: "/bots" })
          queryClient.removeQueries({ queryKey: [...botsKey, bot.name] })
        }}
        done={`${bot.name} removed`}
      />
    </section>
  )
}

// NewBotScreen makes a Bot: its name, then the same form. It opens the new Bot once the server stored it.
export function NewBotScreen() {
  const models = useModels()
  const navigate = useNavigate()

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here="New bot" />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-2xl flex-col gap-5 pt-2">
          <header className="flex flex-col gap-0.5">
            <h2 className="text-lg font-semibold tracking-tight">New bot</h2>
            <p className="text-sm text-muted-foreground">
              A Bot to chat with. It starts with an empty memory.
            </p>
          </header>
          <BotForm
            start={emptyDraft}
            models={models.data ?? []}
            onSaved={(bot) => void navigate({ to: "/bots/$name", params: { name: bot.name } })}
          />
        </div>
      </div>
    </main>
  )
}
