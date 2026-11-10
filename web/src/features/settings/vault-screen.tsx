import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, Eye, EyeOff, KeyRound } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { deleteSecret, getSecretValue, listSecrets, putSecret } from "@/api/generated/sdk.gen"
import type { Secret } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { since } from "@/lib/time"

import { SettingsPage } from "./settings-shell"
import { errorText } from "./use-integrations"

const secretsKey = ["secrets"] as const

const inputClass = "h-9 rounded-md border border-input bg-background px-3 text-sm"
const popupClass =
  "fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100svh-2rem)] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-xl border bg-background p-5 outline-none"
const backdropClass =
  "fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0"

// What a hidden value shows: a fixed run of dots, so its length says nothing (Infisical's HIDDEN_SECRET_VALUE).
const hidden = "••••••••••••"

// VaultScreen is where the owner sees what the vault keeps and reads a value (issue #108). A list comes without
// values; a row's value is fetched only when the owner reveals it, and dropped when he hides it or leaves, as
// Infisical's secrets dashboard does (MIT, Copyright (c) 2022 Infisical Inc., https://github.com/Infisical/infisical;
// frontend/src/pages/secret-manager/OverviewPage/components/SecretTableRow/SecretEditTableRow.tsx fetches with
// `enabled: isVisible`, SecretTableRow.tsx has the eye and copy controls). Changed: no environments, folders, tags
// or comments, and one line a secret instead of a table of environments.
export function VaultScreen() {
  const secrets = useQuery({
    queryKey: secretsKey,
    queryFn: async () => (await listSecrets()).data.data,
  })

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
      {secrets.data?.length === 0 && (
        <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed px-4 py-10 text-center">
          <KeyRound className="size-5 text-muted-foreground" aria-hidden />
          <p className="text-sm font-medium">The vault is empty</p>
          <p className="max-w-sm text-xs text-muted-foreground">
            A token you give an integration is kept here, and so is any secret you add yourself.
          </p>
        </div>
      )}
      {secrets.data && secrets.data.length > 0 && (
        <ul className="flex flex-col divide-y rounded-xl border bg-card">
          {secrets.data.map((secret) => (
            <SecretRow key={secret.name} secret={secret} />
          ))}
        </ul>
      )}
    </SettingsPage>
  )
}

function kindName(secret: Secret): string {
  return secret.kind === "oauth_token_set" ? "Token set" : "Secret"
}

function SecretRow({ secret }: { secret: Secret }) {
  const [shown, setShown] = useState(false)
  const typed = secret.kind === "generic"

  // The value is asked for only while it is shown, and is not kept once hidden.
  const value = useQuery({
    queryKey: ["secret-value", secret.name, secret.version],
    queryFn: async () => (await getSecretValue({ path: { name: secret.name } })).data.values,
    enabled: shown,
    gcTime: 0,
    staleTime: 0,
  })

  return (
    <li className="flex flex-col gap-2 px-4 py-3" aria-label={secret.name}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <KeyRound className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1 basis-56 space-y-0.5">
          <p className="truncate font-mono text-sm font-medium">{secret.name}</p>
          <p className="text-xs text-muted-foreground">
            {kindName(secret)} · version {secret.version} · changed {since(secret.updated_at)}
            {secret.used_by.length > 0 ? ` · used by ${secret.used_by.join(", ")}` : " · not used"}
          </p>
          {secret.key_missing && (
            <p className="text-xs text-needs-you">
              Its root key is not in the keyring, so it cannot be read.
            </p>
          )}
          {secret.login_required && (
            <p className="text-xs text-needs-you">Its sign-in was refused; sign in again.</p>
          )}
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-1.5">
          <Button
            variant="outline"
            size="sm"
            aria-pressed={shown}
            aria-label={shown ? `Hide ${secret.name}` : `Reveal ${secret.name}`}
            onClick={() => setShown((s) => !s)}
          >
            {shown ? <EyeOff /> : <Eye />}
            {shown ? "Hide" : "Reveal"}
          </Button>
          {typed && <ReplaceSecret secret={secret} />}
          <RemoveSecret secret={secret} />
        </div>
      </div>
      <div className="ms-7 flex flex-col gap-1.5">
        {!shown && (
          <p className="font-mono text-sm text-muted-foreground select-none" aria-hidden>
            {hidden}
          </p>
        )}
        {shown && value.isPending && <p className="text-sm text-muted-foreground">Reading…</p>}
        {shown && value.error && (
          <p role="alert" className="text-sm text-destructive">
            {errorText(value.error)}
          </p>
        )}
        {shown &&
          value.data &&
          Object.entries(value.data).map(([key, text]) => (
            <ValueLine
              key={key}
              label={Object.keys(value.data).length > 1 ? key : undefined}
              value={text}
            />
          ))}
      </div>
    </li>
  )
}

// ValueLine is one revealed value with a button that copies it.
function ValueLine({ label, value }: { label?: string; value: string }) {
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
    <div className="flex items-start gap-2">
      <div className="min-w-0 flex-1 space-y-0.5">
        {label && <p className="text-xs text-muted-foreground">{label}</p>}
        <output
          aria-label={label ?? "value"}
          className="block rounded-md border bg-background px-2.5 py-1.5 font-mono text-sm break-all whitespace-pre-wrap"
        >
          {value}
        </output>
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={label ? `Copy ${label}` : "Copy value"}
        onClick={() => void copy()}
      >
        {copied ? <Check /> : <Copy />}
      </Button>
    </div>
  )
}

function useRefreshSecrets() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: secretsKey })
}

// SecretForm is the dialog that keeps a value under a name: a new secret, or a new version of one. What uses the
// secret gets the new value (Omnara: "Rotating a secret updates the value used by its existing references").
function SecretForm({
  trigger,
  title,
  about,
  fixedName,
  action,
}: {
  trigger: React.ReactElement
  title: string
  about: string
  fixedName?: string
  action: string
}) {
  const refresh = useRefreshSecrets()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [value, setValue] = useState("")
  const called = fixedName ?? name.trim()

  const save = useMutation({
    mutationFn: () => putSecret({ path: { name: called }, body: { value } }),
    onSuccess: async () => {
      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setName("")
      setValue("")
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
            {!fixedName && (
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
            <label className="flex flex-col gap-1.5 text-sm">
              Value
              <input
                required
                type="password"
                autoComplete="off"
                value={value}
                onChange={(event) => setValue(event.target.value)}
                className={inputClass}
              />
            </label>
            {save.error && (
              <p role="alert" className="text-sm text-destructive">
                {errorText(save.error)}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
              <Button type="submit" disabled={save.isPending || called === "" || value === ""}>
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
    <SecretForm
      trigger={<Button>Add secret</Button>}
      title="Add a secret"
      about="It is kept encrypted on the server. You can read it again here."
      action="Add"
    />
  )
}

function ReplaceSecret({ secret }: { secret: Secret }) {
  return (
    <SecretForm
      trigger={
        <Button variant="ghost" size="sm" aria-label={`Replace ${secret.name}`}>
          Replace
        </Button>
      }
      title={`Replace ${secret.name}`}
      about={
        secret.used_by.length > 0
          ? `What uses it gets the new value: ${secret.used_by.join(", ")}.`
          : "The new value becomes its next version."
      }
      fixedName={secret.name}
      action="Replace"
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
        render={<Button variant="ghost" size="sm" aria-label={`Remove ${secret.name}`} />}
      >
        Remove
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <Dialog.Title className="text-sm font-medium">Remove {secret.name}?</Dialog.Title>
          <Dialog.Description className="text-sm text-muted-foreground">
            {secret.used_by.length > 0
              ? `It is used by ${secret.used_by.join(", ")}, so the server will refuse until that no longer uses it.`
              : "Every version of it is removed. This cannot be undone."}
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
              Remove secret
            </Button>
          </div>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
