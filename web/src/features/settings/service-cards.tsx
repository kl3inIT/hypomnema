import { useForm } from "@tanstack/react-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ExternalLink, Plug, Plus, RefreshCw, Sheet } from "lucide-react"
import { useState, type ComponentType, type ReactElement, type SVGProps } from "react"
import { z } from "zod"

import "@/api/client"
import {
  beginIntegrationSignIn,
  completeIntegrationSignIn,
  deleteIntegrationConnection,
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
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import { Spinner } from "@/components/ui/spinner"

import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { said, servicesKey } from "./use-integrations"

// The services that sign in with an account (issue #103, step 4): Google is the first. A card a service, as the
// git hosts have theirs, with the accounts the owner signed in under it. A service of kind app (Lark, issue #135)
// has the same card: its client is an app of the service, and it is connected with no sign-in.

function useRefreshServices() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: servicesKey })
}

const marks: Record<string, ComponentType<SVGProps<SVGSVGElement>>> = {
  google: GoogleIcon,
  lark: LarkIcon,
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
  const add = (
    <Button variant="outline" size="sm">
      <Plus data-icon="inline-start" />
      {app ? "Connect app" : "Add account"}
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
                {app ? "Works as your app" : service.services.map(serviceName).join(" · ")}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction>
          <span className="flex flex-wrap items-center justify-end gap-1.5">
            {service.client_id &&
              (app ? (
                // An app is one thing: once it is connected there is no second one to add.
                accounts === 0 && <ConnectAppDialog service={service} trigger={add} />
              ) : (
                <SignInDialog service={service} trigger={add} />
              ))}
            <Badge variant={accounts > 0 && !needsSignIn ? "success" : "warning"}>
              {!service.client_id
                ? "Not set up"
                : accounts === 0
                  ? app
                    ? "Not connected"
                    : "No account"
                  : needsSignIn
                    ? "Needs sign-in"
                    : "Connected"}
            </Badge>
          </span>
        </CardAction>
      </CardHeader>
      {!service.client_id && (
        <CardContent>
          <div className="flex flex-col gap-2 border-t pt-3 text-xs text-muted-foreground">
            <p>
              {app
                ? `${service.name} is connected as an app you create once in its developer console. Give Hypomnema a JSON file with its app_id and app_secret from a terminal:`
                : `${service.name} signs in with an OAuth client you create once. Give its file to Hypomnema from a terminal:`}
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
  const refresh = useRefreshServices()
  const app = service.kind === "app"

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
          </span>
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        {!app && (
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
            app
              ? `Hypomnema stops working as this app. The app's id and secret stay in the vault, for when you connect it again; the app itself is removed in ${service.name}'s developer console.`
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

// ConnectAppDialog connects a service that is an app. There is nothing to sign in to: the owner names the
// Connection, and the control plane checks the app's id and secret with the service and reads the app's name.
function ConnectAppDialog({ service, trigger }: { service: Integration; trigger: ReactElement }) {
  const refresh = useRefreshServices()
  const [open, setOpen] = useState(false)

  const connect = useMutation({
    mutationFn: (label: string) =>
      putIntegrationConnection({ path: { integration: service.id, label } }),
  })

  const form = useForm({
    defaultValues: { label: "" },
    validators: { onSubmit: z.object({ label: labelSchema }) },
    onSubmit: async ({ value }) => {
      try {
        await connect.mutateAsync(value.label)
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
      connect.reset()
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
            <DialogTitle>Connect your {service.name} app</DialogTitle>
            <DialogDescription>
              {`There is nothing to sign in to. Hypomnema checks the app's id and secret with ${service.name} and then works as the app, with what the app is allowed there.`}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <form.Field name="label">
              {(field) => (
                <TextField
                  field={field}
                  label="Name"
                  placeholder="work"
                  description="What you call this app here, such as work."
                />
              )}
            </form.Field>
          </FieldGroup>
          <ErrorAlert error={connect.error} />
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={connect.isPending}>
              {connect.isPending && <Spinner data-icon="inline-start" />}
              Connect
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// What a service is asked for: nothing, to read, or to read and write.
const modes = ["write", "read", "none"] as const

type Mode = (typeof modes)[number]

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
      <DialogContent>
        <form
          autoComplete="off"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            void (started ? finish.handleSubmit() : start.handleSubmit())
          }}
        >
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
                          <NativeSelect
                            id={`access-${id}`}
                            size="sm"
                            value={field.state.value[id] ?? "none"}
                            onChange={(event) =>
                              field.handleChange({
                                ...field.state.value,
                                [id]: event.target.value as Mode,
                              })
                            }
                          >
                            <NativeSelectOption value="write">Read and write</NativeSelectOption>
                            <NativeSelectOption value="read">Read only</NativeSelectOption>
                            <NativeSelectOption value="none">Not connected</NativeSelectOption>
                          </NativeSelect>
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
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={pending}>
              {pending && <Spinner data-icon="inline-start" />}
              {started ? "Connect" : "Continue"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
