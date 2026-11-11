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
import { ConfirmDialog } from "@/features/settings/settings-parts"
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
import { botsKey, useBot, useBots, useDeleteBot, usePutBot } from "./use-bots"

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

// BotScreen is one Bot (issue #169): its name and model, the form for its model and persona, its memory to read,
// and its removal. The page is the Agents pages' profile page (features/agents/profile-screen.tsx).
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

// Memory shows what the Bot keeps across chats, an entry a row. The owner reads it here; the Bot writes it.
function Memory({ bot }: { bot: Bot }) {
  // An entry has no id and two may say the same, so each is keyed by its text and how many times it came before.
  const seen = new Map<string, number>()
  const entries = memoryEntries(bot.memory).map((text) => {
    const nth = seen.get(text) ?? 0
    seen.set(text, nth + 1)

    return { key: `${nth}:${text}`, text }
  })

  return (
    <section aria-labelledby="bot-memory" className="flex flex-col gap-2 border-t pt-4">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <h3 id="bot-memory" className="text-sm font-medium">
          Memory
        </h3>
        <span className="text-xs text-muted-foreground">{memoryLine(bot)}</span>
      </div>
      <p className="text-xs text-muted-foreground">
        What the Bot keeps across its chats. It writes this itself; you read it here.
      </p>
      {entries.length === 0 ? (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
          Nothing kept yet.
        </p>
      ) : (
        <ul
          aria-label="Memory entries"
          className="flex flex-col divide-y rounded-lg border bg-card"
        >
          {entries.map((entry) => (
            <li key={entry.key} className="px-3 py-2 text-sm break-words">
              {entry.text}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// Removal takes a Bot away after a question. The server refuses the first Bot, one with chats and one with a
// memory, and the dialog stays open with its reason.
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
        A Bot can be removed while it has no chats and no memory.
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
      <PageHeader title={<Crumbs here="New Bot" />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-2xl flex-col gap-5 pt-2">
          <header className="flex flex-col gap-0.5">
            <h2 className="text-lg font-semibold tracking-tight">New Bot</h2>
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
