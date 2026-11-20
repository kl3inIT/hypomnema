import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { FileKey, Gauge } from "lucide-react"
import { useId, useRef, useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import { patchIntegrationConnection, putIntegrationConnection } from "@/api/generated/sdk.gen"
import type { Integration, IntegrationConnection } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { ErrorAlert } from "@/components/error-alert"
import { TextField } from "@/components/text-field"
import { said } from "@/lib/said"
import { formatSize, parseSize } from "./bytes"
import { useRefreshServices } from "./use-integrations"

// A BigQuery Connection (issue #976): a Google service account's JSON key, uploaded by the owner, with the GA4
// export dataset it reads, the project its queries are billed to and the cap on the bytes one query may bill. The
// file fields are Metabase's and Grafana's for a key ("Service account JSON file"); the key is read in the browser,
// sent once and never shown: only the file's name is, as Grafana shows "configured" for its private key.

const labelSchema = z
  .string()
  .regex(/^[a-z0-9][a-z0-9-]{0,30}$/, "Lower-case letters, digits and dashes, such as ga4.")

const sizeSchema = z
  .string()
  .refine((s) => s.trim() === "" || parseSize(s) !== undefined, "A size such as 10GiB.")

function serviceAccountSchema(keyNeeded: boolean) {
  return z.object({
    label: labelSchema,
    key: keyNeeded ? z.string().min(1, "Choose the service account's JSON key file.") : z.string(),
    dataset: z
      .string()
      .trim()
      .regex(
        /^[a-z][a-z0-9-]{4,29}\.[A-Za-z0-9_]+$/,
        "The dataset as project.dataset, such as my-app.analytics_123.",
      ),
    billingProject: z
      .string()
      .trim()
      .refine(
        (s) => s === "" || /^[a-z][a-z0-9-]{4,29}$/.test(s),
        "A project id, such as my-billing-project.",
      ),
    location: z.string().trim(),
    maxBytes: sizeSchema,
  })
}

// ConnectServiceAccountDialog connects BigQuery with a service account's key. The control plane checks it step by
// step (the token, the dataset's tables, a native table read once, a dry run in the billing project) and keeps
// nothing when a step fails, which is shown here with the step's name. For a Connection that exists the key may be
// left out: the one kept is checked again with what was changed.
export function ConnectServiceAccountDialog({
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
  const [fileName, setFileName] = useState("")
  const chooser = useRef<HTMLInputElement>(null)
  const schema = serviceAccountSchema(!connection)

  const connect = useMutation({
    mutationFn: (value: z.infer<typeof schema>) =>
      putIntegrationConnection({
        path: { integration: service.id, label: value.label },
        body: {
          datasets: [value.dataset.trim()],
          ...(value.key !== "" && { service_account_key: value.key }),
          ...(value.billingProject.trim() !== "" && {
            billing_project: value.billingProject.trim(),
          }),
          ...(value.location.trim() !== "" && { location: value.location.trim() }),
          ...(value.maxBytes.trim() !== "" && { max_bytes_billed: parseSize(value.maxBytes) }),
        },
      }),
  })

  const settings = connection?.bigquery
  const form = useForm({
    defaultValues: {
      label: connection?.label ?? "",
      key: "",
      dataset: settings?.datasets[0] ?? "",
      billingProject: settings?.billing_project ?? "",
      location: settings?.location ?? "",
      maxBytes: settings ? formatSize(settings.max_bytes_billed) : "",
    },
    validators: { onSubmit: schema },
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
      setFileName("")
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
            Give a new key of a service account made for Hypomnema alone, with BigQuery Data Viewer
            on the GA4 dataset and BigQuery Job User on the billing project, nothing more. Hypomnema
            checks it before keeping anything; the key is kept encrypted in the vault and is not
            shown again.
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
                      placeholder="ga4"
                      description="What you call this account here, such as ga4."
                    />
                  )}
                </form.Field>
              )}
              <form.Field name="key">
                {(field) => {
                  const invalid = field.state.meta.isTouched && !field.state.meta.isValid
                  return (
                    <Field data-invalid={invalid || undefined}>
                      <FieldLabel>Key file</FieldLabel>
                      <input
                        ref={chooser}
                        type="file"
                        accept=".json,application/json"
                        className="hidden"
                        aria-label="Choose the service account's JSON key file"
                        onChange={(event) => {
                          const chosen = event.target.files?.[0]
                          event.target.value = ""
                          if (!chosen) return
                          void chosen.text().then(
                            (text) => {
                              setFileName(chosen.name)
                              field.handleChange(text)
                              field.handleBlur()
                            },
                            () => {
                              setFileName("")
                              field.handleChange("")
                            },
                          )
                        }}
                      />
                      <div className="flex items-center gap-2">
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => chooser.current?.click()}
                        >
                          <FileKey data-icon="inline-start" />
                          Choose file
                        </Button>
                        <span className="text-sm text-muted-foreground">
                          {fileName !== ""
                            ? `${fileName} chosen`
                            : connection
                              ? "The key kept is used"
                              : "No file chosen"}
                        </span>
                      </div>
                      <FieldDescription>
                        The JSON file Google gives when you add a key to the service account.
                      </FieldDescription>
                      {invalid && <FieldError errors={field.state.meta.errors} />}
                    </Field>
                  )
                }}
              </form.Field>
              <form.Field name="dataset">
                {(field) => (
                  <TextField
                    field={field}
                    label="GA4 dataset"
                    placeholder="my-app.analytics_123456789"
                    description="The GA4 export dataset, as project.dataset."
                  />
                )}
              </form.Field>
              <form.Field name="billingProject">
                {(field) => (
                  <TextField
                    field={field}
                    label="Billing project"
                    description="Where queries run and are billed; the key's project when left empty."
                  />
                )}
              </form.Field>
              <form.Field name="location">
                {(field) => (
                  <TextField
                    field={field}
                    label="Location"
                    placeholder="US"
                    description="Where queries run; the dataset's when left empty."
                  />
                )}
              </form.Field>
              <form.Field name="maxBytes">
                {(field) => (
                  <TextField
                    field={field}
                    label="Bytes cap"
                    placeholder="10GiB"
                    description="The most one query may bill on demand; 10GiB when left empty."
                  />
                )}
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

const capSchema = z.object({
  maxBytes: z.string().refine((s) => parseSize(s) !== undefined, "A size such as 10GiB."),
})

// BytesCapDialog changes the cap on the bytes one query of a BigQuery Connection may bill, as
// `hypomnema integrations max-bytes` does.
export function BytesCapDialog({
  service,
  connection,
}: {
  service: Integration
  connection: IntegrationConnection
}) {
  const formId = useId()
  const refresh = useRefreshServices()
  const [open, setOpen] = useState(false)

  const save = useMutation({
    mutationFn: (bytes: number) =>
      patchIntegrationConnection({
        path: { integration: service.id, label: connection.label },
        body: { max_bytes_billed: bytes },
      }),
  })

  const form = useForm({
    defaultValues: {
      maxBytes: connection.bigquery ? formatSize(connection.bigquery.max_bytes_billed) : "",
    },
    validators: { onSubmit: capSchema },
    onSubmit: async ({ value }) => {
      const bytes = parseSize(value.maxBytes) ?? 0
      try {
        await save.mutateAsync(bytes)
      } catch {
        return
      }

      await refresh()
      change(false)
      said(`${connection.label} bills at most ${formatSize(bytes)} a query`)
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
      <DialogTrigger
        render={
          <Button variant="ghost" size="sm">
            <Gauge data-icon="inline-start" />
            Bytes cap
          </Button>
        }
      />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{`Bytes cap of ${connection.label}`}</DialogTitle>
          <DialogDescription>
            The most one query may bill on demand, for its dry run and its job: a query that would
            process more is refused before it runs. It counts BigQuery&apos;s own bytes; it is not a
            daily budget.
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
              <form.Field name="maxBytes">
                {(field) => <TextField field={field} label="Bytes cap" placeholder="10GiB" />}
              </form.Field>
            </FieldGroup>
            <ErrorAlert error={save.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button
            type="submit"
            form={formId}
            focusableWhenDisabled={save.isPending}
            disabled={save.isPending}
          >
            {save.isPending && <Spinner data-icon="inline-start" />}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
