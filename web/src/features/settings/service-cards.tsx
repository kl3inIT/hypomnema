import { useForm } from "@tanstack/react-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ExternalLink, Plug, Plus, RefreshCw, Sheet } from "lucide-react"
import { useId, useState, type ComponentType, type ReactElement, type SVGProps } from "react"
import { z } from "zod"

import "@/api/client"
import {
  beginIntegrationSignIn,
  completeIntegrationSignIn,
  deleteIntegrationConnection,
  patchIntegrationConnection,
  putIntegrationConnection,
} from "@/api/generated/sdk.gen"
import type { Integration, IntegrationConnection, SignIn } from "@/api/generated/types.gen"
import {
  GmailIcon,
  GoogleCalendarIcon,
  GoogleDocsIcon,
  GoogleDriveIcon,
  GoogleIcon,
  LarkIcon,
  SupersetIcon,
} from "@/components/icons/brand-icons"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
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
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field"
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

import { AgentActions } from "./agent-actions"
import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { said, servicesKey } from "./use-integrations"

// The services that sign in with an account (issue #103, step 4): Google is the first. A card a service, as the
// git hosts have theirs, with the accounts the owner signed in under it. A service of kind app (Lark, issue #135)
// has the same card: its client is an app of the service, and it is connected with no sign-in. A service of kind
// account (Superset, issue #203) has it too: an account is the service's address, a user name and a password.

function useRefreshServices() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: servicesKey })
}

const marks: Record<string, ComponentType<SVGProps<SVGSVGElement>>> = {
  google: GoogleIcon,
  lark: LarkIcon,
  superset: SupersetIcon,
}

// IntegrationMark is an Integration's logo, or a plain plug for one that has none here. The Bot page shows it beside
// each Connection a Bot may use.
export function IntegrationMark({ id, className }: { id: string; className?: string }) {
  const Mark = marks[id] ?? Plug

  return <Mark className={className} aria-hidden />
}

// What a service of kind account is for, under its name.
const accountHolds: Record<string, string> = {
  superset: "Dashboards and charts, read as data",
}

type Mark = ComponentType<SVGProps<SVGSVGElement>>

// Each service of an Integration as a person knows it: its name, its mark, and what it holds. A service with no
// mark in the references (Sheets) carries a plain icon.
const about: Record<string, { name: string; holds: string; Mark: Mark }> = {
  gmail: { name: "Gmail", holds: "Mail, drafts and labels", Mark: GmailIcon },
  calendar: { name: "Calendar", holds: "Events and calendars", Mark: GoogleCalendarIcon },
  drive: { name: "Drive", holds: "Files and folders", Mark: GoogleDriveIcon },
  docs: { name: "Docs", holds: "Documents", Mark: GoogleDocsIcon },
  sheets: { name: "Sheets", holds: "Spreadsheets", Mark: Sheet },
}

const serviceName = (id: string) => about[id]?.name ?? id

function ServiceMark({ id }: { id: string }) {
  const Mark = about[id]?.Mark ?? Plug

  return <Mark className="size-4 shrink-0" />
}

// Grant is one thing an account may do, as it is stored: "gmail" may read and write, "gmail:read" may only read.
// It shows as the service's mark and name, and says when it is read only.
function Grant({ grant }: { grant: string }) {
  const [service = "", mode] = grant.split(":")

  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <ServiceMark id={service} />
      {serviceName(service)}
      {mode === "read" && " · read only"}
    </span>
  )
}

export function ServiceCard({ service }: { service: Integration }) {
  const Mark = marks[service.id] ?? Plug
  const accounts = service.connections.length
  const needsSignIn = service.connections.some((c) => c.sign_in_required)
  const app = service.kind === "app"
  // An account at an address of its own needs no client, nor does an app, each account of which is an app of its
  // own (issue #311): both are connected with what the owner types.
  const account = service.kind === "account"
  // A role of an AWS account is connected from a terminal with the key that assumes it (issue #331).
  const role = service.kind === "role"
  const ready = account || app || role || Boolean(service.client_id)
  const add = (
    <Button variant="outline" size="sm">
      <Plus data-icon="inline-start" />
      {app ? "Add app" : role ? "Add role" : "Add account"}
    </Button>
  )

  return (
    <Card size="sm" data-testid="integration-card">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <Mark className="size-6 shrink-0" />
            <span className="flex flex-col gap-0.5">
              <span>{service.name}</span>
              <CardDescription>
                {app
                  ? "Each account works as one of your apps"
                  : role
                    ? "Roles of your AWS accounts, each with the rights its work needs"
                    : account
                      ? (accountHolds[service.id] ?? "An account at its own address")
                      : service.services.map(serviceName).join(" · ")}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction wrap>
          <span className="flex flex-wrap items-center gap-1.5 sm:justify-end">
            {account && <ConnectAccountDialog service={service} trigger={add} />}
            {app && <ConnectAppDialog service={service} trigger={add} />}
            {role && <ConnectRoleDialog service={service} trigger={add} />}
            {!app && !account && !role && service.client_id && (
              <SignInDialog service={service} trigger={add} />
            )}
            <Badge variant={accounts > 0 && !needsSignIn ? "success" : "warning"}>
              {!ready
                ? "Not set up"
                : accounts === 0
                  ? app || role
                    ? "Not connected"
                    : "No account"
                  : needsSignIn
                    ? "Needs sign-in"
                    : "Connected"}
            </Badge>
          </span>
        </CardAction>
      </CardHeader>
      {!ready && (
        <CardContent>
          <div className="flex flex-col gap-2 border-t pt-3 text-xs text-muted-foreground">
            <p>
              {`${service.name} signs in with an OAuth client you create once. Give its file to Hypomnema from a terminal:`}
            </p>
            <code className="font-mono text-foreground">
              hypomnema integrations client set {service.id} --client-file &lt;file&gt;
            </code>
          </div>
        </CardContent>
      )}
      {accounts > 0 && (
        <CardContent>
          <ItemGroup>
            {service.connections.map((connection) => (
              <AccountRow key={connection.label} service={service} connection={connection} />
            ))}
          </ItemGroup>
        </CardContent>
      )}
    </Card>
  )
}

function AccountRow({
  service,
  connection,
}: {
  service: Integration
  connection: IntegrationConnection
}) {
  return (
    <div className="flex flex-col gap-1">
      <AccountItem service={service} connection={connection} />
      <AgentActions service={service} connection={connection} />
    </div>
  )
}

// AccountItem is the account itself: who it is, what it was granted, and what the owner can do with it.
function AccountItem({
  service,
  connection,
}: {
  service: Integration
  connection: IntegrationConnection
}) {
  const refresh = useRefreshServices()
  const app = service.kind === "app"
  const account = service.kind === "account"
  const role = service.kind === "role"

  return (
    <Item variant="muted" size="sm" aria-label={`${service.name} account ${connection.label}`}>
      <ItemContent>
        <ItemTitle>
          {connection.label}
          <span className="font-normal text-muted-foreground">{connection.account}</span>
          {connection.sign_in_required && <Badge variant="warning">Needs sign-in</Badge>}
        </ItemTitle>
        <ItemDescription>
          <span className="flex flex-wrap gap-x-3 gap-y-1">
            {connection.services.map((grant) => (
              <Grant key={grant} grant={grant} />
            ))}
            {app && connection.app_id && <span className="font-mono">{connection.app_id}</span>}
            {role && (
              <>
                <span className="font-mono break-all">{connection.role_arn}</span>
                <span>{`Key ${connection.key ?? ""}`}</span>
                <span>
                  {connection.sign_in_required
                    ? "AWS refused this key the last time"
                    : "The last assume worked"}
                </span>
              </>
            )}
            {account && (
              <>
                <span className="break-all">{connection.address}</span>
                {connection.version && <span>{`${service.name} ${connection.version}`}</span>}
                <span>
                  {connection.sign_in_required
                    ? `${service.name} refused the last sign-in`
                    : "The last sign-in worked"}
                </span>
              </>
            )}
          </span>
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        <ChatsSwitch service={service} connection={connection} />
        {app && (
          <ConnectAppDialog
            service={service}
            connection={connection}
            trigger={
              <Button variant="ghost" size="sm">
                <RefreshCw data-icon="inline-start" />
                Change app
              </Button>
            }
          />
        )}
        {account && (
          <ConnectAccountDialog
            service={service}
            connection={connection}
            trigger={
              <Button variant="ghost" size="sm">
                <RefreshCw data-icon="inline-start" />
                Connect again
              </Button>
            }
          />
        )}
        {role && (
          <ConnectRoleDialog
            service={service}
            connection={connection}
            trigger={
              <Button variant="ghost" size="sm">
                <RefreshCw data-icon="inline-start" />
                Connect again
              </Button>
            }
          />
        )}
        {!app && !account && !role && (
          <SignInDialog
            service={service}
            connection={connection}
            trigger={
              <Button variant="ghost" size="sm">
                <RefreshCw data-icon="inline-start" />
                Sign in again
              </Button>
            }
          />
        )}
        <ConfirmDialog
          trigger={
            <Button variant="ghost" size="sm" aria-label={`Disconnect ${connection.label}`}>
              Disconnect
            </Button>
          }
          title={`Disconnect ${connection.label} (${connection.account})?`}
          consequence={
            role
              ? `Hypomnema forgets this role; agents given it lose it. Its key is removed from the vault when no other role uses it. The role itself stays in your AWS account.`
              : account
                ? `Hypomnema forgets this account's address and removes its password from the vault. The account itself stays at ${service.name}.`
                : app
                  ? `Hypomnema stops working as this app and removes its secret from the vault. The app itself stays in ${service.name}'s developer console.`
                  : `${service.name} is asked to end Hypomnema's access to this account, and its tokens are removed from the vault. If ${service.name} cannot be reached, remove the access in the account's own security settings.`
          }
          action="Disconnect"
          run={() =>
            deleteIntegrationConnection({
              path: { integration: service.id, label: connection.label },
            })
          }
          onDone={refresh}
          done={`${connection.label} was disconnected`}
        />
      </ItemActions>
    </Item>
  )
}

const labelSchema = z
  .string()
  .regex(/^[a-z0-9][a-z0-9-]{0,30}$/, "Lower-case letters, digits and dashes, such as work.")

const appSchema = z.object({
  label: labelSchema,
  appId: z.string().trim().min(1, "The app's App ID, such as cli_…"),
  appSecret: z.string().trim().min(1, "The app's App Secret."),
})

// ConnectAppDialog adds an app of a service that is one (Lark): each account is an app of its own (issue #311,
// as each of OpenClaw's Feishu accounts has its own appId and appSecret). There is nothing to sign in to: the owner
// names the account and gives the app's App ID and App Secret, with the fields the Channels page's setup dialog
// asks them with (channels-screen.tsx, SetUpDialog), and the control plane checks them with the service and reads
// the app's name. For an account that exists the name is fixed and the App ID starts as it is; the secret is typed
// again, as it is never shown.
function ConnectAppDialog({
  service,
  connection,
  trigger,
}: {
  service: Integration
  connection?: IntegrationConnection
  trigger: ReactElement
}) {
  const formId = useId()
  const refresh = useRefreshServices()
  const [open, setOpen] = useState(false)

  const connect = useMutation({
    mutationFn: (value: z.infer<typeof appSchema>) =>
      putIntegrationConnection({
        path: { integration: service.id, label: value.label },
        body: { app_id: value.appId.trim(), app_secret: value.appSecret.trim() },
      }),
  })

  const form = useForm({
    defaultValues: {
      label: connection?.label ?? "",
      appId: connection?.app_id ?? "",
      appSecret: "",
    },
    validators: { onSubmit: appSchema },
    onSubmit: async ({ value }) => {
      try {
        await connect.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(`${service.name} app ${value.label} is connected`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      connect.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {connection ? `Change the app of ${connection.label}` : `Add a ${service.name} app`}
          </DialogTitle>
          <DialogDescription>
            {`The App ID and App Secret are on the app's Credentials page in ${service.name}'s developer console. Hypomnema checks them with ${service.name} and then works as the app, with what the app is allowed there. The secret goes to the vault and is not shown again.`}
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
              {!connection && (
                <form.Field name="label">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="work"
                      description="What you call this app here, such as work or report."
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
            </FieldGroup>
            <ErrorAlert error={connect.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={connect.isPending}>
            {connect.isPending && <Spinner data-icon="inline-start" />}
            Connect
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

const accountSchema = z.object({
  label: labelSchema,
  address: z
    .string()
    .trim()
    .regex(/^https?:\/\/[^\s/?#]+[^\s?#]*$/i, "An address such as https://superset.example.com."),
  username: z.string().trim().min(1, "Give the account's user name."),
  password: z.string().min(1, "Give the account's password."),
})

// ConnectAccountDialog connects a service that is an account at an address of its own (Superset): the address,
// a user name and a password. The control plane signs in there once, which shows that they work, and keeps the
// password in the vault; a refused sign-in keeps nothing and is shown here. For an account that exists the name
// is fixed and the address and user name start as they are; the password is typed again, as it is never shown.
// The arrangement is the Lark dialog's, with the fields both Superset MCP servers ask for.
function ConnectAccountDialog({
  service,
  connection,
  trigger,
}: {
  service: Integration
  connection?: IntegrationConnection
  trigger: ReactElement
}) {
  const formId = useId()
  const refresh = useRefreshServices()
  const [open, setOpen] = useState(false)

  const connect = useMutation({
    mutationFn: (value: z.infer<typeof accountSchema>) =>
      putIntegrationConnection({
        path: { integration: service.id, label: value.label },
        body: {
          address: value.address.trim(),
          username: value.username.trim(),
          password: value.password,
        },
      }),
  })

  const form = useForm({
    defaultValues: {
      label: connection?.label ?? "",
      address: connection?.address ?? "",
      username: connection?.account ?? "",
      password: "",
    },
    validators: { onSubmit: accountSchema },
    onSubmit: async ({ value }) => {
      try {
        await connect.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(`${service.name} account ${value.label} is connected`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      connect.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {connection ? `Connect ${connection.label} again` : `Add a ${service.name} account`}
          </DialogTitle>
          <DialogDescription>
            {`Hypomnema signs in to your ${service.name} with this account and reads what the account may read there. The password is kept encrypted in the vault and is not shown again.`}
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
              {!connection && (
                <form.Field name="label">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="work"
                      description="What you call this account here, such as work."
                    />
                  )}
                </form.Field>
              )}
              <form.Field name="address">
                {(field) => (
                  <TextField
                    field={field}
                    label="Address"
                    placeholder="https://superset.example.com"
                    description={`Where your ${service.name} is, as you open it in a browser.`}
                  />
                )}
              </form.Field>
              <form.Field name="username">
                {(field) => (
                  <TextField
                    field={field}
                    label="User name"
                    description={`An account of ${service.name}'s own, not one that signs in through another service.`}
                  />
                )}
              </form.Field>
              <form.Field name="password">
                {(field) => <TextField field={field} label="Password" secret />}
              </form.Field>
            </FieldGroup>
            <ErrorAlert error={connect.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={connect.isPending}>
            {connect.isPending && <Spinner data-icon="inline-start" />}
            Connect
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

const roleSchema = z.object({
  label: labelSchema,
  roleArn: z
    .string()
    .trim()
    .regex(
      /^arn:aws(-cn|-us-gov)?:iam::\d{12}:role\/\S+$/,
      "A role's ARN, such as arn:aws:iam::123456789012:role/hypomnema/hypomnema-read.",
    ),
  key: labelSchema,
  accessKeyId: z.string().trim(),
  secretAccessKey: z.string().trim(),
})

// ConnectRoleDialog connects a role of an AWS account (issue #331): its ARN and the name of the key that assumes it,
// with the key itself the first time that name is used or to replace it. The control plane assumes the role once,
// which shows the key and the role's trust work, and keeps the key in the vault; a refused assume keeps nothing and
// is shown here. The fields are the AWS CLI's for a role profile (role_arn, source_profile) and for a key.
function ConnectRoleDialog({
  service,
  connection,
  trigger,
}: {
  service: Integration
  connection?: IntegrationConnection
  trigger: ReactElement
}) {
  const formId = useId()
  const refresh = useRefreshServices()
  const [open, setOpen] = useState(false)
  const keys = [...new Set(service.connections.map((c) => c.key).filter(Boolean))]

  const connect = useMutation({
    mutationFn: (value: z.infer<typeof roleSchema>) =>
      putIntegrationConnection({
        path: { integration: service.id, label: value.label },
        body: {
          role_arn: value.roleArn.trim(),
          key: value.key,
          ...(value.accessKeyId.trim() !== "" && {
            access_key_id: value.accessKeyId.trim(),
            secret_access_key: value.secretAccessKey.trim(),
          }),
        },
      }),
  })

  const form = useForm({
    defaultValues: {
      label: connection?.label ?? "",
      roleArn: connection?.role_arn ?? "",
      key: connection?.key ?? keys[0] ?? "main",
      accessKeyId: "",
      secretAccessKey: "",
    },
    validators: { onSubmit: roleSchema },
    onSubmit: async ({ value }) => {
      try {
        await connect.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(`${service.name} role ${value.label} is connected`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      connect.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {connection ? `Connect ${connection.label} again` : `Add an ${service.name} role`}
          </DialogTitle>
          <DialogDescription>
            Agents you give this role act in your AWS account with the rights its policies allow.
            Hypomnema assumes it with a key that may only assume roles; the key is kept encrypted in
            the vault and is not shown again.
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
              {!connection && (
                <form.Field name="label">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="main-read"
                      description="What you call this role here, such as main-read."
                    />
                  )}
                </form.Field>
              )}
              <form.Field name="roleArn">
                {(field) => (
                  <TextField
                    field={field}
                    label="Role ARN"
                    placeholder="arn:aws:iam::123456789012:role/hypomnema/hypomnema-read"
                  />
                )}
              </form.Field>
              <form.Field name="key">
                {(field) => (
                  <TextField
                    field={field}
                    label="Key"
                    description={
                      keys.length > 0
                        ? `The name of the key that assumes it. Kept: ${keys.join(", ")}.`
                        : "The name the key that assumes it is kept under, such as main."
                    }
                  />
                )}
              </form.Field>
              <form.Field name="accessKeyId">
                {(field) => (
                  <TextField
                    field={field}
                    label="Access key ID"
                    description="Only for a key not kept yet, or to replace it."
                  />
                )}
              </form.Field>
              <form.Field name="secretAccessKey">
                {(field) => <TextField field={field} label="Secret access key" secret />}
              </form.Field>
            </FieldGroup>
            <ErrorAlert error={connect.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={connect.isPending}>
            {connect.isPending && <Spinner data-icon="inline-start" />}
            Connect
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ChatsSwitch is the owner's say on whether the coordinator may use an account: off until he turns it on. On, pi
// may use it in any chat and give it to the Runs it creates (issue #103). No agent uses an account by default.
function ChatsSwitch({
  service,
  connection,
}: {
  service: Integration
  connection: IntegrationConnection
}) {
  const refresh = useRefreshServices()
  const id = `chats-${service.id}-${connection.label}`

  const change = useMutation({
    mutationFn: (chats: boolean) =>
      patchIntegrationConnection({
        path: { integration: service.id, label: connection.label },
        body: { chats },
      }),
    onSuccess: async (_, chats) => {
      await refresh()
      said(
        chats
          ? `pi may use ${connection.label} in chats`
          : `pi may no longer use ${connection.label}`,
      )
    },
  })

  return (
    <span className="flex items-center gap-2 text-xs text-muted-foreground">
      <label htmlFor={id}>Use in chats</label>
      <Switch
        id={id}
        size="sm"
        checked={connection.chats}
        disabled={change.isPending}
        onCheckedChange={(next) => change.mutate(next)}
      />
    </span>
  )
}

// What a service is asked for: nothing, to read, or to read and write.
const modes = ["write", "read", "none"] as const

type Mode = (typeof modes)[number]

const modeNames: Record<Mode, string> = {
  write: "Read and write",
  read: "Read only",
  none: "Not connected",
}

const startSchema = z.object({
  label: labelSchema,
  access: z
    .record(z.string(), z.enum(modes))
    .refine((access) => Object.values(access).some((m) => m !== "none"), "Choose a service."),
})

const finishSchema = z.object({
  callback: z
    .string()
    .trim()
    .refine((typed) => typed.includes("code="), "Paste the whole address the browser ended on."),
})

// SignInDialog signs an account in, in two steps, as Hermes' google-workspace skill does with two commands
// (scripts/setup.py: --auth-url, then --auth-code): the owner names the account and what it may do and gets a
// link; he signs in on that page, which ends on an address that does not load, and pastes that address here. For
// an account that exists the label is fixed and its services start as they are.
function SignInDialog({
  service,
  connection,
  trigger,
}: {
  service: Integration
  connection?: IntegrationConnection
  trigger: ReactElement
}) {
  const formId = useId()
  const refresh = useRefreshServices()
  const [open, setOpen] = useState(false)
  const [started, setStarted] = useState<SignIn | null>(null)

  const granted = (id: string): Mode => {
    if (!connection) return "write"
    if (connection.services.includes(id)) return "write"

    return connection.services.includes(`${id}:read`) ? "read" : "none"
  }

  const begin = useMutation({
    mutationFn: async (value: z.infer<typeof startSchema>) =>
      (
        await beginIntegrationSignIn({
          path: { integration: service.id },
          body: {
            label: value.label,
            services: service.services
              .filter((id) => value.access[id] !== "none")
              .map((id) => (value.access[id] === "read" ? `${id}:read` : id)),
          },
        })
      ).data,
  })

  const complete = useMutation({
    mutationFn: (callback: string) =>
      completeIntegrationSignIn({
        path: { integration: service.id, sign_in_id: started?.id ?? "" },
        body: { callback: callback.trim() },
      }),
  })

  const start = useForm({
    defaultValues: {
      label: connection?.label ?? "",
      access: Object.fromEntries(service.services.map((id) => [id, granted(id)])) as Record<
        string,
        Mode
      >,
    },
    validators: { onSubmit: startSchema },
    onSubmit: async ({ value }) => {
      try {
        setStarted(await begin.mutateAsync(value))
      } catch {
        // The dialog stays open and shows what the server refused.
      }
    },
  })

  const finish = useForm({
    defaultValues: { callback: "" },
    validators: { onSubmit: finishSchema },
    onSubmit: async ({ value }) => {
      try {
        await complete.mutateAsync(value.callback)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(`${service.name} account ${start.state.values.label} is connected`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setStarted(null)
      start.reset()
      finish.reset()
      begin.reset()
      complete.reset()
    }
  }

  const pending = begin.isPending || complete.isPending

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>
            {connection ? `Sign ${connection.label} in again` : `Add a ${service.name} account`}
          </DialogTitle>
          <DialogDescription>
            {started
              ? `Sign in on ${service.name}'s page. The browser then ends on an address that does not load: copy that whole address and paste it here.`
              : `Name the account and choose what Hypomnema may do with it. You then sign in on ${service.name}'s own page; your password never passes through here.`}
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
              void (started ? finish.handleSubmit() : start.handleSubmit())
            }}
          >
            {!started && (
              <FieldGroup>
                {!connection && (
                  <start.Field name="label">
                    {(field) => (
                      <TextField
                        field={field}
                        label="Name"
                        placeholder="work"
                        description="What you call this account, such as work or personal."
                      />
                    )}
                  </start.Field>
                )}
                <FieldSet>
                  <FieldLegend variant="label">Access</FieldLegend>
                  <start.Field name="access">
                    {(field) => (
                      <FieldGroup>
                        {service.services.map((id) => (
                          <Field key={id} orientation="horizontal">
                            <ServiceMark id={id} />
                            <FieldContent>
                              <FieldLabel htmlFor={`access-${id}`}>{serviceName(id)}</FieldLabel>
                              <FieldDescription>{about[id]?.holds}</FieldDescription>
                            </FieldContent>
                            <Select
                              value={field.state.value[id] ?? "none"}
                              onValueChange={(mode) =>
                                mode &&
                                field.handleChange({
                                  ...field.state.value,
                                  [id]: mode,
                                })
                              }
                              items={modeNames}
                            >
                              <SelectTrigger id={`access-${id}`} size="sm">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value="write">{modeNames.write}</SelectItem>
                                <SelectItem value="read">{modeNames.read}</SelectItem>
                                <SelectItem value="none">{modeNames.none}</SelectItem>
                              </SelectContent>
                            </Select>
                          </Field>
                        ))}
                        {!field.state.meta.isValid && (
                          <p role="alert" className="text-sm text-destructive">
                            Choose at least one service.
                          </p>
                        )}
                      </FieldGroup>
                    )}
                  </start.Field>
                </FieldSet>
              </FieldGroup>
            )}
            {started && (
              <FieldGroup>
                {/* A link that looks like a button: it leaves this page, so it is an anchor, not a button. */}
                <a
                  href={started.url}
                  target="_blank"
                  rel="noreferrer"
                  className={buttonVariants({ variant: "outline" })}
                >
                  <ExternalLink data-icon="inline-start" />
                  Open {service.name}&apos;s sign-in page
                </a>
                <finish.Field name="callback">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Address the browser ended on"
                      placeholder="http://localhost:1/?state=…&code=…"
                      description={`Within ${Math.round(started.expires_in_seconds / 60)} minutes.`}
                    />
                  )}
                </finish.Field>
              </FieldGroup>
            )}
            <ErrorAlert error={begin.error ?? complete.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={pending}>
            {pending && <Spinner data-icon="inline-start" />}
            {started ? "Connect" : "Continue"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
