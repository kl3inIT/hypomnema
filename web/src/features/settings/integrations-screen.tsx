import { useForm } from "@tanstack/react-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ExternalLink, Plus } from "lucide-react"
import { useState, type ReactNode } from "react"
import { z } from "zod"

import "@/api/client"
import {
  deleteGitHost,
  deleteGitHostToken,
  putGitHost,
  putGitHostToken,
} from "@/api/generated/sdk.gen"
import type { GitHost, GitHostKind } from "@/api/generated/types.gen"
import { GitHubIcon, GitLabIcon } from "@/components/icons/brand-icons"
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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import { Spinner } from "@/components/ui/spinner"

import { ServiceCard } from "./service-cards"
import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { SettingsPage } from "./settings-shell"
import {
  hostName,
  hostOf,
  hostsKey,
  kindName,
  said,
  tokenHelp,
  useHosts,
  useServices,
} from "./use-integrations"

// IntegrationsScreen is where the owner connects the services Hypomnema works with. Today those are the git hosts
// workers push to and open pull requests on; a task tracker would be a second group, as Orca's Integrations pane
// has "Review providers" and "Task providers" (src/renderer/src/components/settings/integrations/IntegrationsPane.tsx).
export function IntegrationsScreen() {
  const hosts = useHosts()
  const services = useServices()

  return (
    <SettingsPage
      title="Integrations"
      description="The services Hypomnema works with, and the access it has to each."
      actions={<AddHost />}
    >
      <section aria-labelledby="services" className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <h2 id="services" className="text-sm font-semibold text-foreground">
            Services
          </h2>
          <p className="text-xs text-muted-foreground">
            Accounts you sign in, for agents to work with on your behalf.
          </p>
        </div>
        <ErrorAlert error={services.error} />
        {services.data?.map((service) => (
          <ServiceCard key={service.id} service={service} />
        ))}
      </section>
      <section aria-labelledby="git-hosts" className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <h2 id="git-hosts" className="text-sm font-semibold text-foreground">
            Git hosts
          </h2>
          <p className="text-xs text-muted-foreground">
            Where workers clone repositories, push their branches and open pull requests.
          </p>
        </div>
        {hosts.isPending && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner />
            Loading…
          </p>
        )}
        <ErrorAlert error={hosts.error} />
        {hosts.data?.map((host) => (
          <HostCard key={host.host} host={host} />
        ))}
      </section>
    </SettingsPage>
  )
}

// HostIcon is the mark of what a host runs: GitHub's or GitLab's. A company's own server carries the mark of the
// product it runs, as its address alone says nothing.
function HostIcon({ host }: { host: GitHost }) {
  const Icon = host.kind === "github" ? GitHubIcon : GitLabIcon

  return <Icon className="size-6 shrink-0" />
}

function HelpLink({ href }: { href: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1 text-xs text-link hover:underline"
    >
      <ExternalLink className="size-3" aria-hidden />
      How to create a token
    </a>
  )
}

const lowerFirst = (text: string) => text.charAt(0).toLowerCase() + text.slice(1)

// HostCard is one service: what it is, the access Hypomnema has, the actions, and what is still to do. The
// arrangement is Orca's IntegrationCardShell and IntegrationCardDetails (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca; src/renderer/src/components/integrations/presentation/integration-card-shell.tsx),
// on the standard Card.
function HostCard({ host }: { host: GitHost }) {
  const refresh = useRefreshHosts()
  const help = tokenHelp(host)

  return (
    <Card size="sm" data-testid="integration-card">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <HostIcon host={host} />
            <span className="flex flex-col gap-0.5">
              <span>{hostName(host)}</span>
              <CardDescription>
                {host.known
                  ? `${host.host} · Pull requests`
                  : `${kindName(host)} server · Pull requests`}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction>
          <span className="flex flex-wrap items-center justify-end gap-1.5">
            <SetToken host={host} />
            {host.has_token && (
              <ConfirmDialog
                trigger={
                  <Button variant="ghost" size="sm">
                    Remove token
                  </Button>
                }
                title={`Remove the token of ${hostName(host)}?`}
                consequence="Workers will use the login of the machine they run on, where it has one. Work already running keeps the token it started with."
                action="Remove token"
                run={() => deleteGitHostToken({ path: { host: host.host } })}
                onDone={refresh}
                done={`The token of ${hostName(host)} was removed`}
              />
            )}
            {!host.known && (
              <ConfirmDialog
                trigger={
                  <Button variant="ghost" size="sm">
                    Remove
                  </Button>
                }
                title={`Remove ${host.host}?`}
                consequence="Its token is removed with it. A host that an open Run or a registered repository is on cannot be removed."
                action="Remove host"
                run={() => deleteGitHost({ path: { host: host.host } })}
                onDone={refresh}
                done={`${host.host} was removed`}
              />
            )}
            <Badge variant={host.has_token ? "success" : "warning"}>
              {host.has_token ? "Connected" : "No token"}
            </Badge>
          </span>
        </CardAction>
      </CardHeader>
      {!host.has_token && (
        <CardContent>
          <div className="flex flex-col gap-2 border-t pt-3 text-xs text-muted-foreground">
            <p>
              Without a token, workers use the login of the machine they run on, where it has one.
            </p>
            <p>To connect: {lowerFirst(help.needs)}</p>
            <HelpLink href={help.docs} />
          </div>
        </CardContent>
      )}
    </Card>
  )
}

function useRefreshHosts() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: hostsKey })
}

// FormDialog is a dialog that holds one form: its title, what it is for, the fields, what the server refused, and
// the two buttons.
function FormDialog({
  open,
  onOpenChange,
  trigger,
  title,
  about,
  error,
  pending,
  action,
  onSubmit,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  trigger: ReactNode
  title: string
  about: string
  error: unknown
  pending: boolean
  action: string
  onSubmit: () => void
  children: ReactNode
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {trigger}
      <DialogContent>
        <form
          autoComplete="off"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            onSubmit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{about}</DialogDescription>
          </DialogHeader>
          {children}
          <ErrorAlert error={error} />
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={pending}>
              {pending && <Spinner data-icon="inline-start" />}
              {action}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

const tokenSchema = z.object({
  token: z
    .string()
    .trim()
    .min(1, "Paste the token.")
    .regex(/^\S+$/, "A token is one line without spaces."),
})

// SetToken is the dialog that takes a host's token, as Orca's credential dialogs take one
// (bitbucket-credentials-dialog.tsx): a password field, what the token needs, and where to make one. The token is
// sent once and never shown again; it lives in this dialog only until it is sent.
function SetToken({ host }: { host: GitHost }) {
  const refresh = useRefreshHosts()
  const [open, setOpen] = useState(false)
  const help = tokenHelp(host)

  const save = useMutation({
    mutationFn: (token: string) =>
      putGitHostToken({ path: { host: host.host }, body: { token: token.trim() } }),
  })

  const form = useForm({
    defaultValues: { token: "" },
    validators: { onSubmit: tokenSchema },
    onSubmit: async ({ value }) => {
      try {
        await save.mutateAsync(value.token)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(`The token of ${hostName(host)} was saved`)
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
    <FormDialog
      open={open}
      onOpenChange={change}
      trigger={
        <DialogTrigger render={<Button variant="outline" size="sm" />}>
          {host.has_token ? "Replace token" : "Add token"}
        </DialogTrigger>
      }
      title={`${host.has_token ? "Replace the token of" : "Add a token for"} ${hostName(host)}`}
      about={`${help.needs} It is kept encrypted on the server; you can read it again in the Vault.`}
      error={save.error}
      pending={save.isPending}
      action="Save token"
      onSubmit={() => void form.handleSubmit()}
    >
      <FieldGroup>
        <form.Field name="token">
          {(field) => <TextField field={field} label="Access token" secret />}
        </form.Field>
      </FieldGroup>
      <HelpLink href={help.docs} />
    </FormDialog>
  )
}

const hostSchema = z.object({
  address: z
    .string()
    .refine(
      (typed) => hostOf(typed).includes("."),
      "Give the server's name, or any URL copied from it.",
    ),
  kind: z.enum(["gitlab", "github"]),
  token: z.string().regex(/^\S*$/, "A token is one line without spaces."),
})

// AddHost declares a company's git server: its address, what it runs, and its token if the owner has it at hand.
function AddHost() {
  const refresh = useRefreshHosts()
  const [open, setOpen] = useState(false)

  const add = useMutation({
    mutationFn: async (value: z.infer<typeof hostSchema>) => {
      const host = hostOf(value.address)

      await putGitHost({ path: { host }, body: { kind: value.kind } })
      if (value.token.trim() !== "") {
        await putGitHostToken({ path: { host }, body: { token: value.token.trim() } })
      }
    },
    // The host may have been declared even when its token was refused.
    onSettled: () => refresh(),
  })

  const form = useForm({
    defaultValues: { address: "", kind: "gitlab" as GitHostKind, token: "" },
    validators: { onSubmit: hostSchema },
    onSubmit: async ({ value }) => {
      try {
        await add.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      change(false)
      said(`${hostOf(value.address)} was added`)
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
    <FormDialog
      open={open}
      onOpenChange={change}
      trigger={
        <DialogTrigger
          render={
            <Button>
              <Plus data-icon="inline-start" />
              Add a git server
            </Button>
          }
        />
      }
      title="Add a git server"
      about="A company's own GitLab or GitHub server. github.com and gitlab.com are already here."
      error={add.error}
      pending={add.isPending}
      action="Add"
      onSubmit={() => void form.handleSubmit()}
    >
      <FieldGroup>
        <form.Field name="address">
          {(field) => (
            <TextField
              field={field}
              label="Address"
              placeholder="gitlab.company.example"
              description="Its name, or any URL copied from it."
            />
          )}
        </form.Field>
        <form.Field name="kind">
          {(field) => (
            <Field>
              <FieldLabel htmlFor={field.name}>It runs</FieldLabel>
              <NativeSelect
                id={field.name}
                name={field.name}
                value={field.state.value}
                onBlur={field.handleBlur}
                onChange={(event) => field.handleChange(event.target.value as GitHostKind)}
              >
                <NativeSelectOption value="gitlab">GitLab</NativeSelectOption>
                <NativeSelectOption value="github">GitHub Enterprise</NativeSelectOption>
              </NativeSelect>
            </Field>
          )}
        </form.Field>
        <form.Field name="token">
          {(field) => (
            <TextField
              field={field}
              label="Access token"
              secret
              placeholder="Optional; it can be added later"
            />
          )}
        </form.Field>
      </FieldGroup>
    </FormDialog>
  )
}
