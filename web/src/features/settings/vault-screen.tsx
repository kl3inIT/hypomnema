import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Pencil,
  Search,
  Trash2,
} from "lucide-react"
import { Fragment, useState } from "react"

import "@/api/client"
import { deleteSecret, getSecretValue, listSecrets, putSecret } from "@/api/generated/sdk.gen"
import type { Secret } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"

import { SettingsPage } from "./settings-shell"
import { errorText } from "./use-integrations"

const secretsKey = ["secrets"] as const

const inputClass = "h-9 rounded-md border border-input bg-background px-3 text-sm"
const popupClass =
  "fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100svh-2rem)] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-xl border bg-background p-5 outline-none"
const backdropClass =
  "fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0"

// A hidden value: a fixed run of asterisks, so its length says nothing (Infisical's HIDDEN_SECRET_VALUE).
const hidden = "************************"

// The values a token set is replaced with, as Omnara's edit dialog asks for its "complete replacement set".
const tokenSetKeys = ["refresh_token", "access_token", "id_token"] as const

function typeName(secret: Secret): string {
  return secret.kind === "oauth_token_set" ? "OAuth token set" : "Generic"
}

// VaultScreen is where the owner sees what the vault keeps and reads a value (issue #108).
//
// From Infisical's secrets dashboard (MIT, Copyright (c) 2022 Infisical Inc.,
// https://github.com/Infisical/infisical; frontend/src/pages/secret-manager/OverviewPage/components/SecretTableRow/,
// docs/images/platform/project/secrets-dashboard-2.png): a table with a Name and a Value column, the value in its
// row, hidden as asterisks; "Reveal Values" in the table's head for every row and an eye a row; a value fetched
// only once it is revealed (`useGetSecretValue(..., { enabled: isVisible })`); a search box and the add button
// above the table.
//
// From Omnara's secrets list (Apache-2.0, https://github.com/omnara-ai/omnara;
// frontend/apps/web/src/components/overview/SecretsSection.tsx, components/org/EditSecretDialog.tsx): the Type
// column, a row that opens to its details (version, payload keys, created, updated), and an edit dialog where "a
// new value replaces" a generic secret and a token set takes "the complete replacement set".
//
// Left out: Infisical's environments, folders, tags, comments and batch editing; Omnara's sorting, paging and
// grants.
export function VaultScreen() {
  const [query, setQuery] = useState("")
  const [revealAll, setRevealAll] = useState(false)
  const secrets = useQuery({
    queryKey: secretsKey,
    queryFn: async () => (await listSecrets()).data.data,
  })

  const all = secrets.data ?? []
  const shown = all.filter((s) => s.name.includes(query.trim().toLowerCase()))

  return (
    <SettingsPage
      title="Vault"
      description="The secrets Hypomnema keeps. Only you can read a value here; agents never can."
      actions={<AddSecret />}
    >
      {secrets.isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {secrets.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(secrets.error)}
        </p>
      )}
      {secrets.data && all.length === 0 && (
        <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed px-4 py-10 text-center">
          <KeyRound className="size-5 text-muted-foreground" aria-hidden />
          <p className="text-sm font-medium">No secrets yet</p>
          <p className="max-w-sm text-xs text-muted-foreground">
            Add the API keys and credentials your providers and integrations use.
          </p>
        </div>
      )}
      {all.length > 0 && (
        <div className="flex min-w-0 flex-col gap-3">
          <label className="flex h-9 items-center gap-2 rounded-md border border-input bg-background px-3 text-sm">
            <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden />
            <span className="sr-only">Search secrets by name</span>
            <input
              type="search"
              autoComplete="off"
              placeholder="Search secrets by name…"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
            />
          </label>
          <div className="relative max-w-full min-w-0 overflow-x-auto rounded-xl border bg-card">
            <table className="w-full min-w-xl text-sm">
              <thead className="border-b text-left text-muted-foreground">
                <tr>
                  <th className="w-8 py-2 ps-3">
                    <span className="sr-only">Details</span>
                  </th>
                  <th className="px-2 py-2 font-medium">Name</th>
                  <th className="w-full px-2 py-2 font-medium">
                    <span className="flex items-center justify-between gap-2">
                      Value
                      <Button
                        variant="ghost"
                        size="xs"
                        aria-pressed={revealAll}
                        onClick={() => setRevealAll((r) => !r)}
                      >
                        {revealAll ? <EyeOff /> : <Eye />}
                        {revealAll ? "Hide values" : "Reveal values"}
                      </Button>
                    </span>
                  </th>
                  <th className="px-2 py-2 font-medium">Type</th>
                  <th className="w-28 py-2 pe-3">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {shown.map((secret) => (
                  <SecretRow key={secret.name} secret={secret} revealAll={revealAll} />
                ))}
                {shown.length === 0 && (
                  <tr>
                    <td colSpan={5} className="px-3 py-6 text-center text-muted-foreground">
                      No secret matches “{query.trim()}”.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </SettingsPage>
  )
}

function SecretRow({ secret, revealAll }: { secret: Secret; revealAll: boolean }) {
  const [revealed, setRevealed] = useState(false)
  const [open, setOpen] = useState(false)
  const shown = revealed || revealAll

  // The value is asked for only while it is shown, and is not kept once hidden.
  const value = useQuery({
    queryKey: ["secret-value", secret.name, secret.version],
    queryFn: async () => (await getSecretValue({ path: { name: secret.name } })).data.values,
    enabled: shown,
    gcTime: 0,
    staleTime: 0,
  })
  const entries = shown && value.data ? Object.entries(value.data) : []

  return (
    <Fragment>
      <tr aria-label={secret.name} className="align-top">
        <td className="py-2 ps-3">
          <Button
            variant="ghost"
            size="icon-xs"
            aria-expanded={open}
            aria-label={open ? `Close ${secret.name}` : `Details of ${secret.name}`}
            onClick={() => setOpen((o) => !o)}
          >
            {open ? <ChevronDown /> : <ChevronRight />}
          </Button>
        </td>
        <td className="px-2 py-2.5 font-medium whitespace-nowrap">{secret.name}</td>
        <td className="px-2 py-2.5 font-mono">
          {!shown && (
            <span className="text-muted-foreground select-none" aria-hidden>
              {hidden}
            </span>
          )}
          {shown && value.isPending && <span className="text-muted-foreground">{hidden}</span>}
          {shown && value.error && (
            <span role="alert" className="font-sans text-destructive">
              {errorText(value.error)}
            </span>
          )}
          {entries.map(([key, text]) => (
            <SecretValue key={key} label={entries.length > 1 ? key : undefined} value={text} />
          ))}
        </td>
        <td className="px-2 py-2.5 whitespace-nowrap text-muted-foreground">{typeName(secret)}</td>
        <td className="py-2 pe-3">
          <span className="flex justify-end gap-0.5">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-pressed={shown}
              aria-label={shown ? `Hide ${secret.name}` : `Reveal ${secret.name}`}
              disabled={revealAll}
              onClick={() => setRevealed((r) => !r)}
            >
              {shown ? <EyeOff /> : <Eye />}
            </Button>
            <EditSecret secret={secret} />
            <RemoveSecret secret={secret} />
          </span>
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={5} className="ps-11 pe-3 pt-0 pb-3">
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-xs">
              <Detail label="Version" value={String(secret.version)} />
              <Detail label="Created" value={new Date(secret.created_at).toLocaleString()} />
              <Detail label="Updated" value={new Date(secret.updated_at).toLocaleString()} />
              {secret.key_missing && (
                <Detail
                  label="Key"
                  value="Its root key is not in the keyring, so it cannot be read."
                />
              )}
              {secret.login_required && (
                <Detail label="Sign-in" value="Refused; sign in again where it is used." />
              )}
            </dl>
          </td>
        </tr>
      )}
    </Fragment>
  )
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{value}</dd>
    </>
  )
}

// SecretValue is one revealed value with a button that copies it.
function SecretValue({ label, value }: { label?: string; value: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
    } catch {
      // No clipboard (an insecure page): the value stays selectable.
    }
  }

  return (
    <span className="flex items-start gap-1.5">
      <span className="min-w-0 flex-1">
        {label && <span className="block font-sans text-xs text-muted-foreground">{label}</span>}
        <output aria-label={label ?? "value"} className="block break-all whitespace-pre-wrap">
          {value}
        </output>
      </span>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label={label ? `Copy ${label}` : "Copy value"}
        onClick={() => void copy()}
      >
        {copied ? <Check /> : <Copy />}
      </Button>
    </span>
  )
}

function useRefreshSecrets() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: secretsKey })
}

function SecretDialog({
  trigger,
  title,
  about,
  secret,
  action,
}: {
  trigger: React.ReactElement
  title: string
  about: string
  secret?: Secret
  action: string
}) {
  const refresh = useRefreshSecrets()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [values, setValues] = useState<Record<string, string>>({})
  const tokenSet = secret?.kind === "oauth_token_set"
  const keys: readonly string[] = tokenSet ? tokenSetKeys : ["value"]
  const called = secret?.name ?? name.trim()
  const required = tokenSet ? "refresh_token" : "value"
  const given = Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ""))

  const save = useMutation({
    mutationFn: () =>
      putSecret({
        path: { name: called },
        body: tokenSet ? { values: given } : { value: given.value ?? "" },
      }),
    onSuccess: async () => {
      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setName("")
      setValues({})
      save.reset()
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Trigger render={trigger} />
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <form
            autoComplete="off"
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              save.mutate()
            }}
          >
            <Dialog.Title className="text-sm font-medium">{title}</Dialog.Title>
            <Dialog.Description className="text-sm text-muted-foreground">
              {about}
            </Dialog.Description>
            {!secret && (
              <div className="flex flex-col gap-1.5 text-sm">
                <label htmlFor="secret-name">Name</label>
                <input
                  id="secret-name"
                  required
                  autoComplete="off"
                  aria-describedby="secret-name-hint"
                  placeholder="openai-key"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  className={inputClass}
                />
                <span id="secret-name-hint" className="text-xs text-muted-foreground">
                  Lower-case letters, digits and dashes.
                </span>
              </div>
            )}
            {keys.map((key) => (
              <label key={key} className="flex flex-col gap-1.5 text-sm">
                {key === "value" ? "Value" : key}
                <input
                  required={key === required}
                  type="password"
                  autoComplete="off"
                  placeholder={secret ? "••••••••••••" : undefined}
                  value={values[key] ?? ""}
                  onChange={(event) => setValues((v) => ({ ...v, [key]: event.target.value }))}
                  className={inputClass}
                />
              </label>
            ))}
            {save.error && (
              <p role="alert" className="text-sm text-destructive">
                {errorText(save.error)}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
              <Button type="submit" disabled={save.isPending || called === "" || !given[required]}>
                {save.isPending ? "Saving…" : action}
              </Button>
            </div>
          </form>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function AddSecret() {
  return (
    <SecretDialog
      trigger={<Button>Add secret</Button>}
      title="New secret"
      about="It is kept encrypted on the server. You can read it again here."
      action="Add"
    />
  )
}

function EditSecret({ secret }: { secret: Secret }) {
  return (
    <SecretDialog
      trigger={
        <Button variant="ghost" size="icon-sm" aria-label={`Edit ${secret.name}`}>
          <Pencil />
        </Button>
      }
      title={`Edit ${secret.name}`}
      about={
        secret.kind === "oauth_token_set"
          ? "To update credentials, enter the complete replacement set."
          : "Enter a new value to replace it."
      }
      secret={secret}
      action="Save"
    />
  )
}

function RemoveSecret({ secret }: { secret: Secret }) {
  const refresh = useRefreshSecrets()
  const [open, setOpen] = useState(false)
  const remove = useMutation({
    mutationFn: () => deleteSecret({ path: { name: secret.name } }),
    onSuccess: async () => {
      await refresh()
      setOpen(false)
    },
  })

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) remove.reset()
      }}
    >
      <Dialog.Trigger
        render={<Button variant="ghost" size="icon-sm" aria-label={`Delete ${secret.name}`} />}
      >
        <Trash2 />
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <Dialog.Title className="text-sm font-medium">Delete {secret.name}?</Dialog.Title>
          <Dialog.Description className="text-sm text-muted-foreground">
            Every version of it is removed. This cannot be undone.
          </Dialog.Description>
          {remove.error && (
            <p role="alert" className="text-sm text-destructive">
              {errorText(remove.error)}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Delete secret
            </Button>
          </div>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
