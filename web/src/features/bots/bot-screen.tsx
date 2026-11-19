import { useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronRight, Trash2 } from "lucide-react"
import { useState } from "react"

import { getBotQueryKey, listBotsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import type { Bot, Model } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button, buttonVariants } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ModelMark } from "@/components/icons/model-mark"
import { AgentAvatar } from "@/components/agent-avatar"
import { ModelField } from "@/features/agents/profile-editor"
import { ModelPicker } from "@/features/agents/model-picker"
import { modelKey } from "@/features/agents/profiles-state"
import { everyCall } from "@/lib/query-keys"
import { errorText } from "@/lib/server-error"
import { useModels } from "@/features/agents/use-profiles"
import { LarkIcon } from "@/components/icons/brand-icons"
import { appNameOf, channelsOf, channelState } from "@/features/settings/channels-state"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { useLarkChannels } from "@/features/settings/use-channels"
import { useServices } from "@/features/settings/use-integrations"
import { BotRoutines } from "@/features/routines/bot-routines"
import { MemoryEditor } from "@/features/memory/memory-editor"
import { MemoryNotes } from "@/features/memory/memory-notes"
import { PageHeader } from "@/components/page-header"
import { SkillsField } from "@/features/skills/skills-field"
import { ToolsField } from "@/features/tools/tools-field"

import { BotAccess } from "./bot-access"
import {
  defaultModelLabel,
  draftOf,
  emptyDraft,
  modelLine,
  nameProblem,
  problem,
  sameDraft,
  settingsOf,
  type Draft,
} from "./bots-state"
import { useBot, useBots, useDeleteBot, usePutBot, useRenameBot, useSetBotMemory } from "./use-bots"

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

// BotScreen is one Bot (issue #169), at its id: its name and model, its name to change (issue #466), the form for
// its model and persona, its memory to read and edit, and its removal. The page is the Agents pages' profile page
// (features/agents/profile-screen.tsx).
export function BotScreen({ id }: { id: string }) {
  const bot = useBot(id)
  const models = useModels()
  const b = bot.data

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here={b?.name ?? ""} />} />
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
                  <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    Bot · <ModelMark name={b.model ?? ""} className="size-3.5 shrink-0" />
                    {modelLine(b)}
                    {b.provider ? ` (${b.provider})` : ""}
                  </p>
                </div>
              </header>

              <Rename key={b.name} bot={b} />
              <BotForm start={draftOf(b)} models={models.data ?? []} stored={b} />
              <Memory bot={b} />
              <BotAccess bot={b} />
              <BotRoutines bot={b.id} />
              <Channels bot={b} />
              <Removal bot={b} />
            </>
          )}
        </div>
      </div>
    </main>
  )
}

// Rename gives the Bot another name (issue #466): its id, chats, Routines and Channels stay, so nothing else on the
// page changes. A name another Bot has is refused here and by the server. The field is the one a node's name has
// on the Machines page.
function Rename({ bot }: { bot: Bot }) {
  const [name, setName] = useState(bot.name)
  const rename = useRenameBot()
  const taken = (useBots().data ?? []).filter((b) => b.id !== bot.id).map((b) => b.name)

  const changed = name.trim() !== bot.name
  const wrong = changed ? nameProblem(name, taken) : null

  return (
    <form
      aria-label="Name"
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        if (changed && !wrong) rename.mutate({ id: bot.id, name: name.trim() })
      }}
    >
      <Field>
        <FieldLabel htmlFor="bot-rename">Name</FieldLabel>
        <div className="flex gap-2">
          <Input
            id="bot-rename"
            autoComplete="off"
            spellCheck={false}
            value={name}
            onChange={(event) => {
              rename.reset()
              setName(event.target.value)
            }}
          />
          <Button
            type="submit"
            variant="outline"
            focusableWhenDisabled={rename.isPending}
            disabled={!changed || wrong !== null || rename.isPending}
          >
            {rename.isPending && <Spinner data-icon="inline-start" />}
            Rename
          </Button>
        </div>
        <FieldDescription>
          {wrong ??
            (bot.main
              ? "The main Bot: a chat that names no Bot talks with it. Its chats, Routines and channels stay when it is renamed."
              : "What the Bot is called. Its chats, Routines and channels stay when it is renamed.")}
        </FieldDescription>
      </Field>
      {rename.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(rename.error)}</AlertDescription>
        </Alert>
      )}
    </form>
  )
}

// BotForm edits a Bot's model, persona and skills, and a new Bot's name, and saves them with one request. Its buttons
// are the profile editor's (features/agents/profile-editor.tsx). Leaving the page drops what was not saved
// (owner, 2026-11-13).
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
  // The names taken: a Bot's name is its own (issue #466).
  const taken = (useBots().data ?? []).map((b) => b.name)

  const dirty = !sameDraft(draft, start)
  const wrong = stored ? null : problem(draft, taken)
  const set = (change: Partial<Draft>) => {
    put.reset()
    setDraft((d) => ({ ...d, ...change }))
  }

  function save() {
    const settings = settingsOf(draft, models)
    if (start.fallbacks.join() !== draft.fallbacks.join()) settings.fallbacks = draft.fallbacks
    put.mutate(
      { id: stored?.id, name: draft.name.trim(), settings },
      {
        onSuccess: (bot) => {
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
            What the Bot is called, up to 64 characters. It can be changed later.
          </FieldDescription>
        </Field>
      )}

      <ModelField
        id="bot-model"
        model={draft.model}
        effort={draft.effort}
        models={models}
        onChange={set}
        defaultLabel={defaultModelLabel}
        description="The model the Bot thinks with. Saving says so when pi cannot use the one chosen."
        gone="No provider offers this model any more. Pick another, or the default."
      />

      <BotFallbacks
        primary={draft.model}
        chain={draft.fallbacks}
        models={models}
        onChange={(fallbacks) => set({ fallbacks })}
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

      <SkillsField
        id="bot-skills"
        access={stored?.tool_access?.skills}
        skills={draft.skills}
        off={draft.skillsOff}
        onChange={set}
        description="What the Bot gets besides the built-in skills, from its next chat on. A skill for everyone is on unless it is turned off here."
      />

      <ToolsField
        id="bot-tools"
        preset={draft.preset}
        access={stored?.tool_access}
        tools={draft.tools}
        off={draft.toolsOff}
        onChange={set}
        description="Hypomnema's tools the Bot may call, at its next call. Its CLI's own tools are not switched here."
      />

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
        <Button
          type="submit"
          focusableWhenDisabled={put.isPending}
          disabled={put.isPending || !dirty || wrong !== null}
        >
          {put.isPending && <Spinner data-icon="inline-start" />}
          {stored ? "Save" : "Create Bot"}
        </Button>
      </div>
    </form>
  )
}

// Memory is what the Bot keeps across chats, an entry a line. The Bot writes it itself, an entry at a time; the
// owner reads it here and edits it by hand (features/memory/memory-editor.tsx).
function BotFallbacks({
  primary,
  chain,
  models,
  onChange,
}: {
  primary: string
  chain: string[]
  models: Model[]
  onChange: (chain: string[]) => void
}) {
  const gateway = models.filter(
    (model) =>
      model.kind === "model_gateway" ||
      model.kind.endsWith("_subscription") ||
      model.kind === "opencode_go",
  )
  const canAdd = gateway.some((model) => modelKey(model.provider, model.name) === primary)
  const free = gateway.filter((model) => {
    const key = modelKey(model.provider, model.name)
    return key !== primary && !chain.includes(key)
  })

  return (
    <Field>
      <FieldLabel htmlFor={chain.length ? "bot-fallback-1" : "bot-fallback-add"}>
        Fallback models
      </FieldLabel>
      {chain.length > 0 && (
        <ol aria-label="Fallback models, in order" className="flex flex-col gap-1.5">
          {chain.map((key, index) => (
            <li key={key} className="flex items-center gap-2">
              <span className="w-3 text-xs text-muted-foreground tabular-nums" aria-hidden>
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <ModelPicker
                  id={`bot-fallback-${index + 1}`}
                  model={key}
                  models={[
                    ...gateway.filter((model) => modelKey(model.provider, model.name) === key),
                    ...free,
                  ]}
                  defaultLabel="Remove fallback"
                  onChange={(next) =>
                    onChange(
                      next
                        ? chain.map((item, at) => (at === index ? next : item))
                        : chain.filter((item) => item !== key),
                    )
                  }
                />
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Remove fallback ${index + 1}`}
                onClick={() => onChange(chain.filter((item) => item !== key))}
              >
                <Trash2 aria-hidden />
              </Button>
            </li>
          ))}
        </ol>
      )}
      {canAdd && free.length > 0 && chain.length < 5 && (
        <ModelPicker
          id="bot-fallback-add"
          model=""
          models={free}
          defaultLabel="Add a fallback…"
          onChange={(next) => {
            if (next) onChange([...chain, next])
          }}
        />
      )}
      <FieldDescription>
        {canAdd
          ? "Tried in order when the previous model has no usable account. Chats that pick their own model skip this list."
          : "Choose a primary model served by the gateway to add fallbacks."}
      </FieldDescription>
    </Field>
  )
}

function Memory({ bot }: { bot: Bot }) {
  const save = useSetBotMemory()

  return (
    <div className="flex flex-col gap-5 border-t pt-4">
      <MemoryEditor
        id="bot-memory"
        label="Memory"
        memory={bot.memory}
        limit={bot.memory_limit}
        writer="The Bot"
        help="What the Bot keeps across its chats, one entry a line. It writes this itself; you can edit it. The Bot sees a change from its next chat on."
        save={{
          mutate: (text, { onSuccess }) =>
            save.mutate({ id: bot.id, memory: text }, { onSuccess: (b) => onSuccess(b.memory) }),
          isPending: save.isPending,
          error: save.error,
          reset: save.reset,
        }}
      />
      <MemoryNotes of={{ bot: bot.id }} />
    </div>
  )
}

// Channels are the doors to this Bot from outside the web client: the Lark Channels that talk to it, each a link
// to the Channels page, where they are set up. Only the owner may read them; for anyone else the list is left
// out.
function Channels({ bot }: { bot: Bot }) {
  const channels = useLarkChannels()
  const services = useServices()
  if (!channels.data) return null

  const mine = channelsOf(bot.id, channels.data)

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
              <Link to="/settings/channels" className="shrink-0 font-medium hover:underline">
                Lark · {channel.name}
              </Link>
              <span className="truncate text-xs text-muted-foreground">
                {appNameOf(channel, services.data)}
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {channelState(channel).label}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// Removal takes a Bot away after a question. The server refuses the main Bot, one with chats, one with a
// memory and one a Channel talks to, and the dialog stays open with its reason.
function Removal({ bot }: { bot: Bot }) {
  const remove = useDeleteBot()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const gone = async () => {
    // The list is read again before the page leaves; this Bot's own query goes with its page.
    await queryClient.invalidateQueries({ queryKey: everyCall(listBotsQueryKey()) })
    await navigate({ to: "/bots" })
    queryClient.removeQueries({ queryKey: getBotQueryKey({ path: { bot_id: bot.id } }) })
  }

  return (
    <section
      aria-label="Remove"
      className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t pt-4"
    >
      <p className="min-w-0 flex-1 basis-64 text-xs text-muted-foreground">
        A Bot can be removed while it has no chats and no memory, and no channel talks to it.
        Removing it with its chats takes those too.
      </p>
      <span className="flex flex-wrap gap-2">
        <ConfirmDialog
          trigger={
            <Button variant="outline" size="sm">
              Remove Bot
            </Button>
          }
          title={`Remove ${bot.name}?`}
          consequence="The Bot, its model and its persona are removed. This cannot be undone."
          action="Remove"
          run={() => remove.mutateAsync({ id: bot.id })}
          onDone={gone}
          done={`${bot.name} removed`}
        />
        {!bot.main && (
          <ConfirmDialog
            trigger={
              <Button variant="destructive" size="sm">
                Remove with its chats
              </Button>
            }
            title={`Remove ${bot.name} and its chats?`}
            consequence={
              "The Bot, its memory, its chats and their messages are removed. Runs and Routines started in " +
              "those chats stay. This cannot be undone."
            }
            action="Remove all"
            run={() => remove.mutateAsync({ id: bot.id, force: true })}
            onDone={gone}
            done={`${bot.name} and its chats removed`}
          />
        )}
      </span>
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
            onSaved={(bot) => void navigate({ to: "/bots/$id", params: { id: bot.id } })}
          />
        </div>
      </div>
    </main>
  )
}
