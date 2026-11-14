import { useForm } from "@tanstack/react-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ExternalLink, Plus } from "lucide-react"
import { useId, useState, type ReactNode } from "react"
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
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { FieldGroup } from "@/components/ui/field"
import { Item, ItemActions, ItemContent, ItemGroup, ItemTitle } from "@/components/ui/item"
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

// IntegrationsScreen is where the owner connects the services Hypomnema works with. The page's header has no
// action: each group carries its own, as Orca's groups and cards do. Today those are the git hosts
// workers push to and open pull requests on; a task tracker would be a second group, as Orca's Integrations pane
// has "Review providers" and "Task providers" (src/renderer/src/components/settings/integrations/IntegrationsPane.tsx).
export function IntegrationsScreen() {
  const hosts = useHosts()
  const services = useServices()

  return (
    <SettingsPage
      title="Integrations"
      description="The services Hypomnema works with, and the access it has to each."
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
        {hosts.data &&
          products.map((product) => (
            <HostCard
              key={product.kind}
              product={product}
              hosts={hosts.data.filter((host) => host.kind === product.kind)}
            />
          ))}
      </section>
    </SettingsPage>
  )
}

// The products a git host runs, in the order their cards are shown: the card's name, what a company's own server
// of it is called, the public host, and what the card holds.
const products: { kind: GitHostKind; name: string; server: string; public: string }[] = [
  { kind: "github", name: "GitHub", server: "GitHub Enterprise", public: "github.com" },
  { kind: "gitlab", name: "GitLab", server: "GitLab", public: "gitlab.com" },
]

type Product = (typeof products)[number]

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

// HostCard is one product, GitHub or GitLab, with its hosts as rows: the public one and the company's own servers
// that run it. A card a product is Orca's (src/renderer/src/components/integrations/source-control/
// cli-source-control-integration-cards.tsx), a row a configured instance Coder's
// (site/src/pages/UserSettingsPage/ExternalAuthPage/ExternalAuthPageView.tsx). The arrangement is Orca's
// IntegrationCardShell and IntegrationCardDetails (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca; src/renderer/src/components/integrations/presentation/integration-card-shell.tsx),
// on the standard Card; a row looks as an account of a Services card does (service-cards.tsx, AccountItem). What a
// token is for and how to make one is said once, at the foot, while a row has none (issue #379): every host of a
// product needs the same token (tokenHelp).
function HostCard({ product, hosts }: { product: Product; hosts: GitHost[] }) {
  const Icon = product.kind === "github" ? GitHubIcon : GitLabIcon
  const missing = hosts.find((host) => !host.has_token)
  const help = missing && tokenHelp(missing)

  return (
    <Card size="sm" data-testid="integration-card">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <Icon className="size-6 shrink-0" />
            <span className="flex flex-col gap-0.5">
              <span>{product.name}</span>
              <CardDescription>
                {`${product.public} and ${product.server} servers · Pull requests`}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction wrap>
          <AddHost product={product} />
        </CardAction>
      </CardHeader>
      {hosts.length > 0 && (
        <CardContent>
          <ItemGroup>
            {hosts.map((host) => (
              <HostRow key={host.host} host={host} />
            ))}
          </ItemGroup>
        </CardContent>
      )}
      {help && (
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

// HostRow is one host of a product: its address, whether Hypomnema has its token, and what the owner can do.
function HostRow({ host }: { host: GitHost }) {
  const refresh = useRefreshHosts()

  return (
    <Item variant="muted" size="sm" aria-label={`${kindName(host)} host ${host.host}`}>
      <ItemContent>
        <ItemTitle>
          {host.host}
          {!host.known && <span className="font-normal text-muted-foreground">server</span>}
        </ItemTitle>
      </ItemContent>
      <ItemActions>
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
              <Button variant="ghost" size="sm" aria-label={`Remove ${host.host}`}>
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
      </ItemActions>
    </Item>
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
  const formId = useId()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {trigger}
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{about}</DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              onSubmit()
            }}
          >
            {children}
            <ErrorAlert error={error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={pending}>
            {pending && <Spinner data-icon="inline-start" />}
            {action}
          </Button>
        </DialogFooter>
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
  token: z.string().regex(/^\S*$/, "A token is one line without spaces."),
})

// AddHost declares a company's git server in the card of the product it runs, as each Services card carries its
// own add action: its address, and its token if the owner has it at hand. What it runs is the card's.
function AddHost({ product }: { product: Product }) {
  const refresh = useRefreshHosts()
  const [open, setOpen] = useState(false)

  const add = useMutation({
    mutationFn: async (value: z.infer<typeof hostSchema>) => {
      const host = hostOf(value.address)

      await putGitHost({ path: { host }, body: { kind: product.kind } })
      if (value.token.trim() !== "") {
        await putGitHostToken({ path: { host }, body: { token: value.token.trim() } })
      }
    },
    // The host may have been declared even when its token was refused.
    onSettled: () => refresh(),
  })

  const form = useForm({
    defaultValues: { address: "", token: "" },
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
            <Button variant="outline" size="sm">
              <Plus data-icon="inline-start" />
              Add server
            </Button>
          }
        />
      }
      title={`Add a ${product.server} server`}
      about={`A company's own ${product.server} server. ${product.public} is already here.`}
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
              placeholder={`${product.kind}.company.example`}
              description="Its name, or any URL copied from it."
            />
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
