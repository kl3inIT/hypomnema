import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { Plus } from "lucide-react"
import { useId, useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import {
  addLarkChannelPerson,
  deleteLarkChannel,
  putLarkChannel,
  removeLarkChannelPerson,
} from "@/api/generated/sdk.gen"
import type { LarkChannel, LarkChannelMode, LarkChannelPerson } from "@/api/generated/types.gen"
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
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { firstBot } from "@/features/bots/bots-state"
import { useBots } from "@/features/bots/use-bots"

import { botChoices, channelState, modeNames, nameProblem } from "./channels-state"
import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { SettingsPage } from "./settings-shell"
import { useLarkChannels, useRefreshChannels } from "./use-channels"
import { said } from "./use-integrations"

// Channels are the doors to the Bots from outside the web client (issues #136 and #169). A Lark Channel is a
// Lark app, the Bot it talks to and who may talk through it; there may be several, and several may talk to one
// Bot. One card a Channel, as an Integration has its card: what it is, whether it is connected, its Bot, who may
// talk through it. The page does what `hypomnema channels lark ...` does.

export function ChannelsScreen() {
  const channels = useLarkChannels()
  const bots = (useBots().data ?? []).map((bot) => bot.name)
  const list = channels.data ?? []
  const taken = list.map((channel) => channel.name)

  return (
    <SettingsPage
      title="Channels"
      description="Where you talk to your Bots from, outside this page. A channel is a door to one Bot."
      actions={
        list.length > 0 && (
          <SetUpDialog
            bots={bots}
            taken={taken}
            trigger={
              <Button size="sm">
                <Plus data-icon="inline-start" />
                Add channel
              </Button>
            }
          />
        )
      }
    >
      <ErrorAlert error={channels.error} />
      {channels.isPending ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      ) : list.length === 0 ? (
        !channels.error && <NoChannelCard bots={bots} />
      ) : (
        list.map((channel) => (
          <LarkChannelCard key={channel.name} channel={channel} bots={bots} taken={taken} />
        ))
      )}
    </SettingsPage>
  )
}

// NoChannelCard is the page before the first Channel: what a Lark Channel needs, and the way to set one up.
function NoChannelCard({ bots }: { bots: string[] }) {
  return (
    <Card size="sm" data-testid="channel-empty">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <LarkIcon className="size-6 shrink-0" />
            <span className="flex flex-col gap-0.5">
              <span>Lark</span>
              <CardDescription>
                A message to a Lark app of yours is a message to a Bot
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction>
          <Badge variant="warning">Not set up</Badge>
        </CardAction>
      </CardHeader>
      <CardContent>
        <div className="flex flex-col gap-3 border-t pt-3 text-xs text-muted-foreground">
          <p>
            A channel is a Lark app of yours with the bot feature, the event Receive message and the
            card callback, both over a persistent connection. Give Hypomnema its App ID and App
            Secret, your email at Lark, and the Bot it talks to.
          </p>
          <span>
            <SetUpDialog
              bots={bots}
              taken={[]}
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
    </Card>
  )
}

function LarkChannelCard({
  channel,
  bots,
  taken,
}: {
  channel: LarkChannel
  bots: string[]
  taken: string[]
}) {
  const refresh = useRefreshChannels()
  const now = channelState(channel)
  const id = `lark-${channel.name}`

  const change = useMutation({
    mutationFn: (body: { mode?: LarkChannelMode; enabled?: boolean; bot?: string }) =>
      putLarkChannel({ path: { channel: channel.name }, body }),
    onSuccess: refresh,
  })

  return (
    <Card size="sm" data-testid="channel-card" aria-label={`Channel ${channel.name}`}>
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <LarkIcon className="size-6 shrink-0" />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="truncate">{channel.name}</span>
              <CardDescription>
                A message to this Lark app is a message to {channel.bot}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction>
          <span className="flex flex-wrap items-center justify-end gap-2">
            <span className="flex items-center gap-2 text-xs text-muted-foreground">
              <label htmlFor={`${id}-on`}>On</label>
              <Switch
                id={`${id}-on`}
                size="sm"
                checked={channel.enabled}
                disabled={change.isPending}
                onCheckedChange={(enabled) =>
                  change.mutate(
                    { enabled },
                    {
                      onSuccess: () =>
                        said(enabled ? `${channel.name} is on` : `${channel.name} is off`),
                    },
                  )
                }
              />
            </span>
            <Badge variant={now.good ? "success" : "warning"}>{now.label}</Badge>
          </span>
        </CardAction>
      </CardHeader>
      <CardContent>
        <div className="flex flex-col gap-3 border-t pt-3">
          <ErrorAlert error={change.error} />
          {channel.enabled && !channel.connected && (
            <output className="text-xs text-muted-foreground">
              {channel.last_error
                ? `It could not connect: ${channel.last_error}`
                : "It connects a moment after being switched on."}
            </output>
          )}
          {channel.connected_app_id && (
            <output className="text-xs text-muted-foreground">
              Still connected as the app {channel.connected_app_id}: the new app is used once the
              control plane starts again.
            </output>
          )}
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-xs text-muted-foreground">
            <span>
              App <span className="font-mono text-foreground">{channel.app_id}</span>
            </span>
            <span className="flex flex-wrap items-center gap-2">
              <label htmlFor={`${id}-bot`} className="whitespace-nowrap">
                Bot
              </label>
              <Select
                value={channel.bot}
                disabled={change.isPending}
                onValueChange={(bot) => {
                  if (!bot) return

                  change.mutate(
                    { bot },
                    { onSuccess: () => said(`${channel.name} talks to ${bot}`) },
                  )
                }}
              >
                <SelectTrigger id={`${id}-bot`} size="sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {botChoices(bots, channel.bot).map((bot) => (
                    <SelectItem key={bot} value={bot}>
                      {bot}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </span>
            <span className="flex flex-wrap items-center gap-2">
              <label htmlFor={`${id}-mode`} className="whitespace-nowrap">
                Who may talk to it
              </label>
              <Select
                value={channel.mode}
                disabled={change.isPending}
                onValueChange={(mode) => mode && change.mutate({ mode })}
                items={modeNames}
              >
                <SelectTrigger id={`${id}-mode`} size="sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(modeNames) as LarkChannelMode[]).map((mode) => (
                    <SelectItem key={mode} value={mode}>
                      {modeNames[mode]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </span>
          </div>
          <p className="text-xs text-muted-foreground">
            Someone you let in can give work. Questions for approval come to you alone. A chat that
            is open keeps its Bot; the next one is with the Bot chosen here.
          </p>
          <People channel={channel} />
          <span className="flex flex-wrap gap-2">
            <SetUpDialog
              channel={channel}
              bots={bots}
              taken={taken}
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
              title={`Remove the channel ${channel.name}?`}
              consequence="Hypomnema forgets the channel, the people you added to it and its app's secret, and takes no more messages from its Lark app. The chats stay. The app itself is removed in Lark's developer console."
              action="Remove"
              done={`The channel ${channel.name} is removed`}
              run={() => deleteLarkChannel({ path: { channel: channel.name } })}
              onDone={refresh}
            />
          </span>
        </div>
      </CardContent>
    </Card>
  )
}

// People are who the owner added to a Channel. They count only while the mode is the allowlist, and the list
// says so.
function People({ channel }: { channel: LarkChannel }) {
  const refresh = useRefreshChannels()

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs font-medium text-foreground">
          People you added
          {channel.mode !== "allowlist" && (
            <span className="font-normal text-muted-foreground">
              {" "}
              · used when you choose “{modeNames.allowlist}”
            </span>
          )}
        </p>
        <AddPersonDialog
          channel={channel.name}
          trigger={
            <Button variant="outline" size="sm">
              <Plus data-icon="inline-start" />
              Add person
            </Button>
          }
        />
      </div>
      {channel.people.length === 0 ? (
        <p className="text-xs text-muted-foreground">Nobody yet.</p>
      ) : (
        <ItemGroup>
          {channel.people.map((person) => (
            <PersonRow
              key={person.open_id}
              channel={channel.name}
              person={person}
              onDone={refresh}
            />
          ))}
        </ItemGroup>
      )}
    </div>
  )
}

function PersonRow({
  channel,
  person,
  onDone,
}: {
  channel: string
  person: LarkChannelPerson
  onDone: () => Promise<unknown>
}) {
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
          consequence="They can no longer give work through this channel while only the people you add may talk to it. Their chat stays."
          action="Remove"
          done={`${shown} is removed`}
          run={() => removeLarkChannelPerson({ path: { channel, person: person.open_id } })}
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

function AddPersonDialog({ channel, trigger }: { channel: string; trigger: ReactElement }) {
  const formId = useId()
  const refresh = useRefreshChannels()
  const [open, setOpen] = useState(false)

  const add = useMutation({
    mutationFn: (who: string) =>
      addLarkChannelPerson({ path: { channel }, body: { who: who.trim() } }),
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
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>Add a person</DialogTitle>
          <DialogDescription>
            They must be a member of the app&apos;s Lark organization. Hypomnema looks them up at
            Lark with the app. A person is added to each channel apart.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <FieldGroup>
              <form.Field name="who">
                {(field) => (
                  <TextField field={field} label="Email at Lark" placeholder="linh@company.com" />
                )}
              </form.Field>
            </FieldGroup>
            <ErrorAlert error={add.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={add.isPending}>
            {add.isPending && <Spinner data-icon="inline-start" />}
            Add
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

const setUpSchema = z.object({
  name: z.string(),
  appId: z.string().trim().min(1, "The app's App ID, such as cli_…"),
  appSecret: z.string().trim().min(1, "The app's App Secret."),
  owner: z.string().trim(),
  bot: z.string(),
})

// SetUpDialog gives Hypomnema a Channel's app. For a new Channel it also takes its name, who the owner is at
// Lark and the Bot it talks to; for one that exists it only replaces the app. The secret is typed into a
// password field, goes to the vault and is never shown again.
function SetUpDialog({
  channel,
  bots,
  taken,
  trigger,
}: {
  // channel is the Channel whose app is changed; absent for a new one.
  channel?: LarkChannel
  bots: string[]
  // taken are the names of the Channels there are.
  taken: string[]
  trigger: ReactElement
}) {
  const formId = useId()
  const refresh = useRefreshChannels()
  const [open, setOpen] = useState(false)

  const save = useMutation({
    mutationFn: (value: z.infer<typeof setUpSchema>) =>
      putLarkChannel({
        path: { channel: channel?.name ?? value.name.trim() },
        body: {
          app_id: value.appId.trim(),
          app_secret: value.appSecret.trim(),
          owner: value.owner.trim() || undefined,
          ...(!channel && { bot: value.bot }),
        },
      }),
  })

  const form = useForm({
    defaultValues: {
      // The first Channel is named as the one the control plane made of the Lark bot there was.
      name: channel || taken.length > 0 ? "" : "main",
      appId: channel?.app_id ?? "",
      appSecret: "",
      owner: "",
      bot: firstBot,
    },
    validators: {
      onSubmit: setUpSchema.superRefine((value, ctx) => {
        if (channel) return

        const wrong = nameProblem(value.name, taken)
        if (wrong) ctx.addIssue({ code: "custom", path: ["name"], message: wrong })

        if (value.owner.trim() === "") {
          ctx.addIssue({ code: "custom", path: ["owner"], message: "Your email at Lark." })
        }
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
        <DialogHeader>
          <DialogTitle>
            {channel ? `Change the app of ${channel.name}` : "Add a Lark channel"}
          </DialogTitle>
          <DialogDescription>
            {channel
              ? "Another app is used once the control plane starts again. The secret goes to the vault and is not shown again."
              : "The App ID and App Secret are on the app's Credentials page in Lark's developer console. The secret goes to the vault and is not shown again. One app is one channel. A new channel is off and takes messages from you only."}
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <FieldGroup>
              {!channel && (
                <form.Field name="name">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="support"
                      description="Lowercase letters, digits and dashes. It cannot be changed later."
                    />
                  )}
                </form.Field>
              )}
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
                      channel
                        ? "Leave empty to stay the owner as you are."
                        : "Hypomnema looks you up at Lark with the app, which also shows the App ID and Secret work."
                    }
                  />
                )}
              </form.Field>
              {!channel && (
                <form.Field name="bot">
                  {(field) => (
                    <Field>
                      <FieldLabel htmlFor={field.name}>Bot</FieldLabel>
                      <Select
                        name={field.name}
                        value={field.state.value}
                        onValueChange={(bot) => bot && field.handleChange(bot)}
                      >
                        <SelectTrigger id={field.name} width="full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {botChoices(bots, firstBot).map((bot) => (
                            <SelectItem key={bot} value={bot}>
                              {bot}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <FieldDescription>
                        The Bot a message through this channel talks to.
                      </FieldDescription>
                    </Field>
                  )}
                </form.Field>
              )}
            </FieldGroup>
            <ErrorAlert error={save.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
