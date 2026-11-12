import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus } from "lucide-react"
import { useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import {
  addLarkBotPerson,
  deleteLarkBot,
  getLarkBot,
  putLarkBot,
  removeLarkBotPerson,
} from "@/api/generated/sdk.gen"
import type { LarkBot, LarkBotMode, LarkBotPerson } from "@/api/generated/types.gen"
import { LarkIcon } from "@/components/icons/brand-icons"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { FieldGroup } from "@/components/ui/field"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"

import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { SettingsPage } from "./settings-shell"
import { said } from "./use-integrations"

// Channels are the places outside the web client the owner gives work from (issue #136): the Lark bot is the
// first. One card a channel, as an Integration has its card: what it is, whether it is connected, who may talk to
// it. The page does what `hypomnema channels lark ...` does.

const botKey = ["lark-bot"] as const

// A bot that is not set up answers 404, which is an answer here and not a failure.
function useLarkBot() {
  return useQuery({
    queryKey: botKey,
    queryFn: async () => {
      const answer = await getLarkBot({ throwOnError: false })
      if (answer.response?.status === 404) return null
      if (answer.error) throw answer.error

      return answer.data ?? null
    },
  })
}

function useRefreshBot() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: botKey })
}

export function ChannelsScreen() {
  const bot = useLarkBot()

  return (
    <SettingsPage
      title="Channels"
      description="Where you give Hypomnema work from, outside this page."
    >
      <ErrorAlert error={bot.error} />
      {bot.isPending ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      ) : (
        <LarkBotCard bot={bot.data ?? null} />
      )}
    </SettingsPage>
  )
}

const modeNames: Record<LarkBotMode, string> = {
  owner: "Only you",
  allowlist: "You and the people you add",
  everyone: "Everyone in your Lark organization",
}

function state(bot: LarkBot | null): { label: string; good: boolean } {
  if (!bot) return { label: "Not set up", good: false }
  if (!bot.enabled) return { label: "Off", good: false }
  if (bot.connected) return { label: "Connected", good: true }

  return { label: "Not connected", good: false }
}

function LarkBotCard({ bot }: { bot: LarkBot | null }) {
  const refresh = useRefreshBot()
  const now = state(bot)

  const change = useMutation({
    mutationFn: (body: { mode?: LarkBotMode; enabled?: boolean }) => putLarkBot({ body }),
    onSuccess: refresh,
  })

  return (
    <Card size="sm" data-testid="channel-card">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <LarkIcon className="size-6 shrink-0" />
            <span className="flex flex-col gap-0.5">
              <span>Lark bot</span>
              <CardDescription>
                A message to the bot is a message to the coordinator
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction>
          <span className="flex flex-wrap items-center justify-end gap-2">
            {bot && (
              <span className="flex items-center gap-2 text-xs text-muted-foreground">
                <label htmlFor="lark-bot-on">On</label>
                <Switch
                  id="lark-bot-on"
                  size="sm"
                  checked={bot.enabled}
                  disabled={change.isPending}
                  onCheckedChange={(enabled) =>
                    change.mutate(
                      { enabled },
                      {
                        onSuccess: () =>
                          said(enabled ? "The Lark bot is on" : "The Lark bot is off"),
                      },
                    )
                  }
                />
              </span>
            )}
            <Badge variant={now.good ? "success" : "warning"}>{now.label}</Badge>
          </span>
        </CardAction>
      </CardHeader>
      {!bot && (
        <CardContent>
          <div className="flex flex-col gap-3 border-t pt-3 text-xs text-muted-foreground">
            <p>
              The bot is a Lark app of yours with the bot feature, the event Receive message and the
              card callback, both over a persistent connection. Give Hypomnema its App ID and App
              Secret, and your email at Lark.
            </p>
            <span>
              <SetUpDialog
                trigger={
                  <Button variant="outline" size="sm">
                    <Plus data-icon="inline-start" />
                    Set up
                  </Button>
                }
              />
            </span>
          </div>
        </CardContent>
      )}
      {bot && (
        <CardContent>
          <div className="flex flex-col gap-3 border-t pt-3">
            <ErrorAlert error={change.error} />
            {bot.enabled && !bot.connected && (
              <output className="text-xs text-muted-foreground">
                {bot.last_error
                  ? `It could not connect: ${bot.last_error}`
                  : "It connects within half a minute of being switched on."}
              </output>
            )}
            {bot.connected_app_id && (
              <output className="text-xs text-muted-foreground">
                Still connected as the app {bot.connected_app_id}: the new app is used once the
                control plane starts again.
              </output>
            )}
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-xs text-muted-foreground">
              <span>
                App <span className="font-mono text-foreground">{bot.app_id}</span>
              </span>
              <span className="flex flex-wrap items-center gap-2">
                <label htmlFor="lark-bot-mode" className="whitespace-nowrap">
                  Who may talk to it
                </label>
                <NativeSelect
                  id="lark-bot-mode"
                  size="sm"
                  value={bot.mode}
                  disabled={change.isPending}
                  onChange={(event) => change.mutate({ mode: event.target.value as LarkBotMode })}
                >
                  {(Object.keys(modeNames) as LarkBotMode[]).map((mode) => (
                    <NativeSelectOption key={mode} value={mode}>
                      {modeNames[mode]}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </span>
            </div>
            <p className="text-xs text-muted-foreground">
              Someone you let in can give work. Questions for approval come to you alone.
            </p>
            <People bot={bot} />
            <span className="flex flex-wrap gap-2">
              <SetUpDialog
                bot={bot}
                trigger={
                  <Button variant="outline" size="sm">
                    Change app
                  </Button>
                }
              />
              <ConfirmDialog
                trigger={
                  <Button variant="ghost" size="sm">
                    Remove
                  </Button>
                }
                title="Remove the Lark bot?"
                consequence="Hypomnema forgets the bot and its app's secret, and takes no more messages from Lark. The people you added and the chats stay, for a bot you set up again. The app itself is removed in Lark's developer console."
                action="Remove"
                done="The Lark bot is removed"
                run={() => deleteLarkBot()}
                onDone={refresh}
              />
            </span>
          </div>
        </CardContent>
      )}
    </Card>
  )
}

// People are who the owner added. They count only while the mode is the allowlist, and the list says so.
function People({ bot }: { bot: LarkBot }) {
  const refresh = useRefreshBot()

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs font-medium text-foreground">
          People you added
          {bot.mode !== "allowlist" && (
            <span className="font-normal text-muted-foreground">
              {" "}
              · used when you choose “{modeNames.allowlist}”
            </span>
          )}
        </p>
        <AddPersonDialog
          trigger={
            <Button variant="outline" size="sm">
              <Plus data-icon="inline-start" />
              Add person
            </Button>
          }
        />
      </div>
      {bot.people.length === 0 ? (
        <p className="text-xs text-muted-foreground">Nobody yet.</p>
      ) : (
        <ItemGroup>
          {bot.people.map((person) => (
            <PersonRow key={person.open_id} person={person} onDone={refresh} />
          ))}
        </ItemGroup>
      )}
    </div>
  )
}

function PersonRow({ person, onDone }: { person: LarkBotPerson; onDone: () => Promise<unknown> }) {
  const shown = person.name || person.email || person.open_id

  return (
    <Item variant="muted" size="sm" aria-label={`Person ${shown}`}>
      <ItemContent>
        <ItemTitle>{shown}</ItemTitle>
        <ItemDescription>{person.email || person.open_id}</ItemDescription>
      </ItemContent>
      <ItemActions>
        <ConfirmDialog
          trigger={
            <Button variant="ghost" size="sm" aria-label={`Remove ${shown}`}>
              Remove
            </Button>
          }
          title={`Remove ${shown}?`}
          consequence="They can no longer give work through the bot while only the people you add may talk to it. Their chat stays."
          action="Remove"
          done={`${shown} is removed`}
          run={() => removeLarkBotPerson({ path: { person: person.open_id } })}
          onDone={onDone}
        />
      </ItemActions>
    </Item>
  )
}

const personSchema = z.object({
  who: z
    .string()
    .trim()
    .refine(
      (typed) => typed.includes("@") || typed.startsWith("ou_"),
      "Their email at Lark, or their open_id (ou_…).",
    ),
})

function AddPersonDialog({ trigger }: { trigger: ReactElement }) {
  const refresh = useRefreshBot()
  const [open, setOpen] = useState(false)

  const add = useMutation({
    mutationFn: (who: string) => addLarkBotPerson({ body: { who: who.trim() } }),
  })

  const form = useForm({
    defaultValues: { who: "" },
    validators: { onSubmit: personSchema },
    onSubmit: async ({ value }) => {
      try {
        await add.mutateAsync(value.who)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      add.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <form
          autoComplete="off"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <DialogHeader>
            <DialogTitle>Add a person</DialogTitle>
            <DialogDescription>
              They must be a member of the app&apos;s Lark organization. Hypomnema looks them up at
              Lark with the app.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <form.Field name="who">
              {(field) => (
                <TextField field={field} label="Email at Lark" placeholder="linh@company.com" />
              )}
            </form.Field>
          </FieldGroup>
          <ErrorAlert error={add.error} />
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={add.isPending}>
              {add.isPending && <Spinner data-icon="inline-start" />}
              Add
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

const setUpSchema = z.object({
  appId: z.string().trim().min(1, "The app's App ID, such as cli_…"),
  appSecret: z.string().trim().min(1, "The app's App Secret."),
  owner: z.string().trim(),
})

// SetUpDialog gives Hypomnema the bot's app: the first time with who the owner is at Lark, later to use another
// app. The secret is typed into a password field, goes to the vault and is never shown again.
function SetUpDialog({ bot, trigger }: { bot?: LarkBot; trigger: ReactElement }) {
  const refresh = useRefreshBot()
  const [open, setOpen] = useState(false)

  const save = useMutation({
    mutationFn: (value: z.infer<typeof setUpSchema>) =>
      putLarkBot({
        body: {
          app_id: value.appId.trim(),
          app_secret: value.appSecret.trim(),
          owner: value.owner.trim() || undefined,
        },
      }),
  })

  const form = useForm({
    defaultValues: { appId: bot?.app_id ?? "", appSecret: "", owner: "" },
    validators: {
      onSubmit: setUpSchema.refine((value) => bot || value.owner.trim() !== "", {
        path: ["owner"],
        message: "Your email at Lark.",
      }),
    },
    onSubmit: async ({ value }) => {
      try {
        await save.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      save.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <form
          autoComplete="off"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{bot ? "Change the bot's app" : "Set the Lark bot up"}</DialogTitle>
            <DialogDescription>
              {bot
                ? "Another app is used once the control plane starts again. The secret goes to the vault and is not shown again."
                : "The App ID and App Secret are on the app's Credentials page in Lark's developer console. The secret goes to the vault and is not shown again. A new bot is off and takes messages from you only."}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <form.Field name="appId">
              {(field) => <TextField field={field} label="App ID" placeholder="cli_…" />}
            </form.Field>
            <form.Field name="appSecret">
              {(field) => <TextField field={field} label="App Secret" secret />}
            </form.Field>
            <form.Field name="owner">
              {(field) => (
                <TextField
                  field={field}
                  label="Your email at Lark"
                  placeholder="you@company.com"
                  description={
                    bot
                      ? "Leave empty to stay the owner as you are."
                      : "Hypomnema looks you up at Lark with the app, which also shows the App ID and Secret work."
                  }
                />
              )}
            </form.Field>
          </FieldGroup>
          <ErrorAlert error={save.error} />
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending && <Spinner data-icon="inline-start" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
