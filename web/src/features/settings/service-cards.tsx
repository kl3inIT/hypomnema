import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Copy, ExternalLink, Plug, Plus, RefreshCw, Smartphone, UserRound } from "lucide-react"
import QRCode from "qrcode"
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ComponentType,
  type ReactElement,
  type SVGProps,
} from "react"
import { z } from "zod"

import "@/api/client"
import {
  beginIntegrationSignIn,
  completeIntegrationSignIn,
  deleteIntegrationConnection,
  putIntegrationConnection,
} from "@/api/generated/sdk.gen"
import {
  getIntegrationSignInOptions,
  patchIntegrationConnectionMutation,
  syncMessagingFromPhoneMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import type {
  DeviceSession,
  Integration,
  IntegrationConnection,
  PhoneSync,
  SignIn,
} from "@/api/generated/types.gen"
import {
  GmailIcon,
  GoogleCalendarIcon,
  GoogleDocsIcon,
  GoogleDriveIcon,
  GoogleIcon,
  GoogleSearchConsoleIcon,
  GoogleSheetsIcon,
  LarkIcon,
  AwsIcon,
  SupersetIcon,
  WhatsAppIcon,
  ZaloIcon,
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
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { TextField } from "@/components/text-field"
import { said } from "@/lib/said"
import { useRefreshServices } from "./use-integrations"

// The services that sign in with an account (issue #103, step 4): Google is the first. A card a service, as the
// git hosts have theirs, with the accounts the owner signed in under it. A service of kind app (Lark, issue #135)
// has the same card: its client is an app of the service, and it is connected with no sign-in. A service of kind
// account (Superset, issue #203) has it too: an account is the service's address, a user name and a password.

const marks: Record<string, ComponentType<SVGProps<SVGSVGElement>>> = {
  google: GoogleIcon,
  lark: LarkIcon,
  superset: SupersetIcon,
  aws: AwsIcon,
  whatsapp: WhatsAppIcon,
  zalo: ZaloIcon,
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

// Each service of an Integration as a person knows it: its name, its mark, and what it holds.
const about: Record<string, { name: string; holds: string; Mark: Mark }> = {
  gmail: { name: "Gmail", holds: "Mail, drafts and labels", Mark: GmailIcon },
  calendar: { name: "Calendar", holds: "Events and calendars", Mark: GoogleCalendarIcon },
  drive: { name: "Drive", holds: "Files and folders", Mark: GoogleDriveIcon },
  docs: { name: "Docs", holds: "Documents", Mark: GoogleDocsIcon },
  sheets: { name: "Sheets", holds: "Spreadsheets", Mark: GoogleSheetsIcon },
  searchconsole: {
    name: "Search Console",
    holds: "Sites, sitemaps, search performance and indexing",
    Mark: GoogleSearchConsoleIcon,
  },
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
  // The owner's own messaging account, linked as a device of his by a QR code his phone scans (issue #615).
  const device = service.kind === "device"
  const ready = account || app || role || device || Boolean(service.client_id)
  // The owner may sign in to an app service as himself through one of its apps (issue #468): a user Connection.
  const apps = service.connections.filter((c) => !c.app)
  const add = (
    <Button variant="outline" size="sm">
      <Plus data-icon="inline-start" />
      {app ? "Add app" : role ? "Add role" : device ? "Link phone" : "Add account"}
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
                  ? "Each account works as one of your apps, or as you through one"
                  : role
                    ? "Roles of your AWS accounts, each with the rights its work needs"
                    : device
                      ? "Your own account, linked as a device of your phone"
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
            {app && apps.length > 0 && (
              <UserSignInDialog
                service={service}
                trigger={
                  <Button variant="outline" size="sm">
                    <UserRound data-icon="inline-start" />
                    Sign in as me
                  </Button>
                }
              />
            )}
            {role && <ConnectRoleDialog service={service} trigger={add} />}
            {device && <LinkDeviceDialog service={service} trigger={add} />}
            {!app && !account && !role && !device && service.client_id && (
              <SignInDialog service={service} trigger={add} />
            )}
            <Badge variant={accounts > 0 && !needsSignIn ? "success" : "warning"}>
              {!ready
                ? "Not set up"
                : accounts === 0
                  ? app || role
                    ? "Not connected"
                    : device
                      ? "Not linked"
                      : "No account"
                  : needsSignIn
                    ? device
                      ? "Needs a link"
                      : "Needs sign-in"
                    : device
                      ? "Linked"
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
  // The owner's own sign-in through one of the service's apps (issue #468), beside the apps themselves.
  const user = Boolean(connection.app)
  const app = service.kind === "app" && !user
  const account = service.kind === "account"
  const role = service.kind === "role"
  const device = service.kind === "device"

  return (
    <Item variant="muted" size="sm" aria-label={`${service.name} account ${connection.label}`}>
      <ItemContent>
        <ItemTitle>
          {connection.label}
          <span className="font-normal text-muted-foreground">{connection.account}</span>
          {connection.sign_in_required && (
            <Badge variant="warning">{device ? "Needs a link" : "Needs sign-in"}</Badge>
          )}
        </ItemTitle>
        <ItemDescription>
          <span className="flex flex-wrap gap-x-3 gap-y-1">
            {connection.services.map((grant) => (
              <Grant key={grant} grant={grant} />
            ))}
            {app && connection.app_id && <span className="font-mono">{connection.app_id}</span>}
            {user && (
              <>
                <span className="inline-flex items-center gap-1.5">
                  <UserRound className="size-4 shrink-0" aria-hidden />
                  {`You, through ${connection.app}`}
                </span>
                <span>Only a Bot given it, when you ask it yourself</span>
              </>
            )}
            {role && (
              <>
                <RoleName arn={connection.role_arn ?? ""} />
                <span>{`Key ${connection.key ?? ""}`}</span>
                <span>
                  {connection.sign_in_required
                    ? "AWS refused this key the last time"
                    : "The last assume worked"}
                </span>
              </>
            )}
            {device && connection.session && <span>{sessionSays(connection.session)}</span>}
            {device && connection.session?.phone_sync && (
              <span>{phoneSyncSays(connection.session.phone_sync)}</span>
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
        <div className="flex flex-wrap items-center gap-2">
          <ChatsSwitch service={service} connection={connection} />
          {device && phoneSyncs.has(service.id) && connection.session?.state === "connected" && (
            <PhoneSyncButton service={service} connection={connection} />
          )}
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
          {device && (
            <LinkDeviceDialog
              service={service}
              connection={connection}
              trigger={
                <Button variant="ghost" size="sm">
                  <RefreshCw data-icon="inline-start" />
                  Link again
                </Button>
              }
            />
          )}
          {user && (
            <UserSignInDialog
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
          {!app && !account && !role && !device && !user && (
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
              user
                ? `${service.name} is asked to end your sign-in, and its tokens are removed from the vault; no Bot acts as you any more. If ${service.name} cannot be reached, remove the app's access in your own account.`
                : device
                  ? (forgetHow[service.id] ??
                    `The device is logged out of your ${service.name}, so your phone's linked devices drop it, and Hypomnema forgets its session. Your account itself is not touched.`)
                  : role
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
        </div>
      </ItemActions>
    </Item>
  )
}

// sessionSays is a linked account's session in a few words, as MemoryOS's runbook explains each state
// (docs/runbooks/whatsapp-bot.md, "When it stops").
function sessionSays(session: DeviceSession): string {
  switch (session.state) {
    case "connected":
      return "Connected"
    case "connecting":
      return "Connecting"
    case "disconnected":
      return "Disconnected; it reconnects by itself"
    case "logged_out":
      return "Logged out on the phone; link it again"
    case "banned":
      return session.until
        ? `Banned by WhatsApp until ${new Date(session.until).toLocaleString()}`
        : "Banned by WhatsApp for a while"
    case "replaced":
      return "Another client opened this session"
    case "outdated":
      return "WhatsApp refuses this version; update Hypomnema"
  }
}

// phoneSyncs are the services whose past comes from the owner's phone in one sync: Zalo's servers keep none of it
// for a web session (issue #697), so it is asked of the phone as Zalo PC's "Sync messages" asks.
const phoneSyncs = new Set(["zalo"])

// syncing are the stages of a sync that is under way.
const syncing = new Set<PhoneSync["stage"]>(["asking", "sending", "importing"])

// phoneSyncSays is the last sync from the phone in a few words.
function phoneSyncSays(sync: PhoneSync): string {
  switch (sync.stage) {
    case "asking":
      return "Confirm the sync on your phone"
    case "sending":
      return "Your phone is sending its messages"
    case "importing":
      return "Keeping the messages from your phone"
    case "done":
      return `${sync.messages.toLocaleString()} messages from your phone kept, in ${sync.chats.toLocaleString()} chats${sync.skipped ? `; ${sync.skipped} chats could not be read` : ""}`
    case "failed":
      return `The sync from your phone failed: ${sync.reason ?? "no reason given"}`
  }
}

// PhoneSyncButton asks the owner's phone for every chat's past, and follows the sync until it ends.
function PhoneSyncButton({
  service,
  connection,
}: {
  service: Integration
  connection: IntegrationConnection
}) {
  const refresh = useRefreshServices()
  const stage = connection.session?.phone_sync?.stage
  const busy = stage !== undefined && syncing.has(stage)

  const sync = useMutation({
    ...syncMessagingFromPhoneMutation(),
    onSuccess: async () => {
      await refresh()
      said("Confirm the sync on your phone")
    },
  })

  // The sync goes on at the phone and on the server: its stage is read again until it ends.
  useEffect(() => {
    if (!busy) return
    const timer = window.setInterval(() => void refresh(), 3000)

    return () => window.clearInterval(timer)
  }, [busy, refresh])

  return (
    <>
      {sync.error && <span className="text-xs text-destructive">{String(sync.error)}</span>}
      <Button
        variant="ghost"
        size="sm"
        disabled={busy || sync.isPending}
        onClick={() => sync.mutate({ path: { integration: service.id, label: connection.label } })}
      >
        {busy || sync.isPending ? (
          <Spinner data-icon="inline-start" />
        ) : (
          <Smartphone data-icon="inline-start" />
        )}
        Sync from phone
      </Button>
    </>
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
          <Button
            type="submit"
            form={formId}
            focusableWhenDisabled={connect.isPending}
            disabled={connect.isPending}
          >
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
          <Button
            type="submit"
            form={formId}
            focusableWhenDisabled={connect.isPending}
            disabled={connect.isPending}
          >
            {connect.isPending && <Spinner data-icon="inline-start" />}
            Connect
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// RoleName is a role as the AWS console lists it: its name, with the whole ARN on hover and a button that copies
// it (the console shows a role's name in its list and its ARN, with a copy button, on the role's own page).
function RoleName({ arn }: { arn: string }) {
  const name = arn.slice(arn.lastIndexOf("/") + 1)

  async function copy() {
    try {
      await navigator.clipboard.writeText(arn)
      said("Role ARN copied")
    } catch {
      // No clipboard (an insecure page): the ARN is still in the title.
    }
  }

  return (
    <span className="inline-flex min-w-0 items-center gap-1">
      <span className="truncate" title={arn}>{`Role ${name}`}</span>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Copy the role's ARN"
        onClick={() => void copy()}
      >
        <Copy />
      </Button>
    </span>
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
          <Button
            type="submit"
            form={formId}
            focusableWhenDisabled={connect.isPending}
            disabled={connect.isPending}
          >
            {connect.isPending && <Spinner data-icon="inline-start" />}
            Connect
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

const linkSchema = z.object({ label: labelSchema })

// LinkDeviceDialog links the owner's own messaging account (WhatsApp) as a device of his phone (issue #615): he
// names it, then scans the QR code in WhatsApp › Linked devices › Link a device, as MemoryOS's runbook has it
// (docs/runbooks/whatsapp-bot.md, "Linking"). The code changes about every 20 seconds, so the dialog reads the
// current one meanwhile, as MemoryOS's bot page refreshes its code, and waits for the scan as UserSignInDialog
// waits for its code. Linking an account again replaces its device.
function LinkDeviceDialog({
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
  // Each linking the owner starts; closing the dialog moves it on, so a late answer of a closed one is dropped.
  const attempt = useRef(0)

  const begin = useMutation({
    mutationFn: async (value: z.infer<typeof linkSchema>) =>
      (
        await beginIntegrationSignIn({
          path: { integration: service.id },
          body: { label: value.label },
        })
      ).data,
  })

  const complete = useMutation({
    mutationFn: (id: string) =>
      completeIntegrationSignIn({ path: { integration: service.id, sign_in_id: id }, body: {} }),
  })

  const current = useQuery({
    ...getIntegrationSignInOptions({
      path: { integration: service.id, sign_in_id: started?.id ?? "" },
    }),
    enabled: started !== null && complete.isPending,
    refetchInterval: 5000,
  })

  const form = useForm({
    defaultValues: { label: connection?.label ?? "main" },
    validators: { onSubmit: linkSchema },
    onSubmit: async ({ value }) => {
      const mine = ++attempt.current
      let signIn: SignIn
      try {
        signIn = await begin.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }
      if (attempt.current !== mine) return

      setStarted(signIn)

      try {
        await complete.mutateAsync(signIn.id)
      } catch {
        return // The dialog shows why, and the owner starts again.
      }

      await refresh()
      if (attempt.current !== mine) return
      change(false)
      said(`${service.name} ${value.label} is linked`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      attempt.current++
      setStarted(null)
      form.reset()
      begin.reset()
      complete.reset()
    }
  }

  function again() {
    attempt.current++
    setStarted(null)
    begin.reset()
    complete.reset()
  }

  const code = current.data?.qr_code ?? started?.qr_code
  // Zalo gives its QR code as a picture, with nothing to draw it from (issue #657).
  const image = current.data?.qr_image ?? started?.qr_image
  const waiting = started !== null && complete.isPending

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {connection ? `Link ${connection.label} again` : `Link your ${service.name}`}
          </DialogTitle>
          <DialogDescription>
            {started
              ? (scanHow[service.id] ??
                `On your phone, open ${service.name} › Linked devices › Link a device, and scan this code.`)
              : (introHow[service.id] ??
                `Your phone links Hypomnema as one of its devices, as ${service.name} Web is. The main Bot can then read and write in your chats as you. ${service.name} forbids clients it did not make, so a number can be banned; Hypomnema never sends in bulk.`)}
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
            {!started && !connection && (
              <FieldGroup>
                <form.Field name="label">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="main"
                      description="What you call this account here, such as main."
                    />
                  )}
                </form.Field>
              </FieldGroup>
            )}
            {started && waiting && (code || image) && (
              <div className="flex flex-col items-center gap-3">
                {code ? (
                  <QrCode text={code} label={`QR code to link ${service.name}`} />
                ) : (
                  <img
                    src={image}
                    alt={`QR code to link ${service.name}`}
                    className="size-56 rounded-md bg-white p-2"
                  />
                )}
                <output className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Spinner />
                  Waiting for your phone to scan it
                </output>
              </div>
            )}
            <ErrorAlert error={begin.error ?? complete.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          {started && complete.isError ? (
            <Button onClick={again}>Start again</Button>
          ) : (
            !started && (
              <Button
                type="submit"
                form={formId}
                focusableWhenDisabled={begin.isPending}
                disabled={begin.isPending}
              >
                {begin.isPending ? (
                  <Spinner data-icon="inline-start" />
                ) : (
                  <Smartphone data-icon="inline-start" />
                )}
                Show the code
              </Button>
            )
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// introHow says what linking gives, for a service whose words differ from WhatsApp's: Zalo keeps only the chats from
// the link on, as its servers give no past (issues #669, #680).
const introHow: Record<string, string> = {
  zalo: "Your phone logs Hypomnema in, as Zalo Web is, and every chat is kept from then on. The main Bot can then read and write in them as you, each message it sends after your approval. Zalo forbids clients it did not make, so an account can be locked; Hypomnema never sends in bulk.",
}

// forgetHow says what disconnecting does, for a service whose phone cannot be told to drop the login: Zalo has no
// call for it (issue #657).
const forgetHow: Record<string, string> = {
  zalo: "Hypomnema forgets the session and the chats it kept. Zalo cannot be told to end the login: remove it on your phone in Zalo › Settings › Account and security › Login devices.",
}

// scanHow says where each service's app scans a link code, when it is not WhatsApp's Linked devices.
const scanHow: Record<string, string> = {
  zalo: "On your phone, open Zalo, tap the QR scanner at the top, scan this code, then confirm the login.",
}

// QrCode draws a QR code with qrcode's own encoder (MIT; OpenClaw's web login draws its WhatsApp code with the same
// package), one square a dark module.
function QrCode({ text, label }: { text: string; label: string }) {
  const { modules } = QRCode.create(text, { errorCorrectionLevel: "M" })
  const quiet = 2
  const size = modules.size + quiet * 2
  let path = ""
  for (let y = 0; y < modules.size; y++) {
    for (let x = 0; x < modules.size; x++) {
      if (modules.get(x, y)) path += `M${x + quiet} ${y + quiet}h1v1h-1z`
    }
  }

  // Black on white whatever the theme, as a phone's camera reads it.
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="white"/><path d="${path}" fill="black"/></svg>`

  return (
    <img
      src={`data:image/svg+xml,${encodeURIComponent(svg)}`}
      alt={label}
      className="size-64 rounded-md"
    />
  )
}

const userSchema = z.object({
  label: labelSchema,
  app: z.string().min(1, "Choose the app to sign in through."),
})

// UserSignInDialog signs the owner in to an app service (Lark) as himself, through one of the apps he connected
// (issue #468), as lark-cli's `auth login` does by device code (cmd/auth/login.go): he names the account and the
// app and is given a link and a code, which he enters on any device, the phone he is signed in on at Lark first.
// The dialog waits for it meanwhile, as `integrations connect lark --as-user` does. Its steps are SignInDialog's.
// For an account that exists the name is fixed and its app starts as it is.
function UserSignInDialog({
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
  const apps = service.connections.filter((c) => !c.app)

  const begin = useMutation({
    mutationFn: async (value: z.infer<typeof userSchema>) =>
      (
        await beginIntegrationSignIn({
          path: { integration: service.id },
          body: { label: value.label, app: value.app },
        })
      ).data,
  })

  const complete = useMutation({
    mutationFn: (id: string) =>
      completeIntegrationSignIn({ path: { integration: service.id, sign_in_id: id }, body: {} }),
  })

  const form = useForm({
    defaultValues: {
      label: connection?.label ?? "me",
      app: connection?.app ?? apps[0]?.label ?? "",
    },
    validators: { onSubmit: userSchema },
    onSubmit: async ({ value }) => {
      let signIn: SignIn
      try {
        signIn = await begin.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      setStarted(signIn)

      try {
        await complete.mutateAsync(signIn.id)
      } catch {
        return // The dialog shows why, and the owner starts again.
      }

      await refresh()
      change(false)
      said(`${service.name} account ${value.label} is connected as you`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setStarted(null)
      form.reset()
      begin.reset()
      complete.reset()
    }
  }

  function again() {
    setStarted(null)
    begin.reset()
    complete.reset()
  }

  async function copy(code: string) {
    try {
      await navigator.clipboard.writeText(code)
      said("Code copied")
    } catch {
      // No clipboard (an insecure page): the code is on the screen to type.
    }
  }

  const appNames = Object.fromEntries(apps.map((a) => [a.label, `${a.label} (${a.account})`]))
  const waiting = started !== null && complete.isPending

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {connection
              ? `Sign ${connection.label} in again`
              : `Sign in to ${service.name} as yourself`}
          </DialogTitle>
          <DialogDescription>
            {started
              ? `Open the link on any device where you are signed in to ${service.name}, and enter the code there.`
              : `A Bot given it then reads what you can read in ${service.name}, only when you ask it yourself. Anything it would send or change as you waits for your yes. Your password never passes through here.`}
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
            {!started && (
              <FieldGroup>
                {!connection && (
                  <form.Field name="label">
                    {(field) => (
                      <TextField
                        field={field}
                        label="Name"
                        placeholder="me"
                        description="What you call your own account here, such as me."
                      />
                    )}
                  </form.Field>
                )}
                <form.Field name="app">
                  {(field) => (
                    <Field>
                      <FieldLabel htmlFor={`${formId}-app`}>App</FieldLabel>
                      <Select
                        value={field.state.value}
                        onValueChange={(next) => next && field.handleChange(next)}
                        items={appNames}
                      >
                        <SelectTrigger id={`${formId}-app`}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {apps.map((a) => (
                            <SelectItem key={a.label} value={a.label}>
                              {appNames[a.label]}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <FieldDescription>
                        {`The app needs "Refresh user_access_token" on in its security settings at ${service.name}.`}
                      </FieldDescription>
                    </Field>
                  )}
                </form.Field>
              </FieldGroup>
            )}
            {started && (
              <FieldGroup>
                <Field>
                  <FieldLabel>Code</FieldLabel>
                  <span className="flex items-center gap-2">
                    <span className="font-mono text-2xl font-semibold tracking-widest">
                      {started.user_code}
                    </span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label="Copy the code"
                      onClick={() => void copy(started.user_code ?? "")}
                    >
                      <Copy />
                    </Button>
                  </span>
                </Field>
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
                {waiting && (
                  <output className="flex items-center gap-2 text-sm text-muted-foreground">
                    <Spinner />
                    {`Waiting for the code, for ${Math.max(1, Math.round(started.expires_in_seconds / 60))} minutes`}
                  </output>
                )}
              </FieldGroup>
            )}
            <ErrorAlert error={begin.error ?? complete.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          {started && complete.isError ? (
            <Button onClick={again}>Start again</Button>
          ) : (
            !started && (
              <Button
                type="submit"
                form={formId}
                focusableWhenDisabled={begin.isPending}
                disabled={begin.isPending}
              >
                {begin.isPending && <Spinner data-icon="inline-start" />}
                Continue
              </Button>
            )
          )}
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
    ...patchIntegrationConnectionMutation(),
    onSuccess: async (_, { body: { chats } }) => {
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
      <label htmlFor={id} className="whitespace-nowrap">
        Use in chats
      </label>
      <Switch
        id={id}
        size="sm"
        checked={connection.chats}
        disabled={change.isPending}
        onCheckedChange={(chats) =>
          change.mutate({
            path: { integration: service.id, label: connection.label },
            body: { chats },
          })
        }
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
